package webrtc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	pion "github.com/pion/webrtc/v4"
)

// DataChannelLabel is the label of the reliable, ordered work-dispatch data
// channel created between two Helix edge peers.
const DataChannelLabel = "helix-dispatch"

// Security/DoS hardening bounds. These cap the resources a single hostile or
// misbehaving remote peer can pin or grow on this side of the data channel.
const (
	// maxInboundMessageBytes is the hard cap on the size of a single inbound
	// data-channel message we will accept before decoding. A frame larger than
	// this is rejected (and the channel closed) rather than handed to the codec,
	// so a hostile peer cannot drive an out-of-memory by announcing a giant
	// length prefix / payload. It is set above the codec's per-field cap
	// (maxFieldLen, 16 MiB) plus header/framing overhead so any legitimately
	// encodable WorkEnvelope still fits, while a 4x-or-more oversize frame is
	// refused.
	maxInboundMessageBytes = maxFieldLen + (1 << 20) // 16 MiB + 1 MiB framing slack

	// sctpMaxMessageSize bounds the largest message the SCTP/data-channel layer
	// will reassemble before delivering it to OnMessage. This is the real
	// allocation guard at the transport layer: without it, pion buffers up to
	// its default before our handler ever runs. We pin it to our application
	// inbound cap so the transport refuses oversize reassembly itself.
	sctpMaxMessageSize uint32 = maxInboundMessageBytes

	// sctpMaxReceiveBufferSize bounds total SCTP receive-buffer memory per
	// association, capping how much unread inbound data can accumulate when the
	// consumer is slow.
	sctpMaxReceiveBufferSize uint32 = 4 << 20 // 4 MiB

	// recvQueueDepth bounds the number of decoded, not-yet-consumed envelopes
	// held in memory. Past this depth the non-blocking OnMessage handler drops
	// (and surfaces an error) rather than growing without bound or blocking the
	// pion read callback.
	recvQueueDepth = 64

	// iceDisconnectedTimeout / iceFailedTimeout reap half-open or stalled
	// ICE/DTLS handshakes instead of pinning goroutines indefinitely. A peer
	// that never completes the handshake transitions to failed within
	// iceFailedTimeout and its resources can be released.
	iceDisconnectedTimeout = 5 * time.Second
	iceFailedTimeout       = 10 * time.Second
	iceKeepAliveInterval   = 2 * time.Second

	// stunGatherTimeout bounds how long ICE candidate gathering blocks, so a
	// peer that cannot gather candidates does not pin the connection open.
	stunGatherTimeout = 5 * time.Second
)

// ErrChannelNotOpen is returned by Send when the data channel has not yet
// reached the open state.
var ErrChannelNotOpen = errors.New("webrtc: data channel not open")

// ErrMessageTooLarge is surfaced via Receive when an inbound data-channel
// message exceeds maxInboundMessageBytes. The offending channel is closed.
var ErrMessageTooLarge = errors.New("webrtc: inbound message exceeds max size")

// hardenedAPI builds a pion API whose SettingEngine applies the security/DoS
// bounds above (SCTP message + receive-buffer caps, ICE/DTLS reaping timeouts,
// bounded STUN gathering). All Peers are constructed through it so the limits
// are enforced uniformly and out of the box.
func hardenedAPI() *pion.API {
	var se pion.SettingEngine
	se.SetSCTPMaxMessageSize(sctpMaxMessageSize)
	se.SetSCTPMaxReceiveBufferSize(sctpMaxReceiveBufferSize)
	se.SetICETimeouts(iceDisconnectedTimeout, iceFailedTimeout, iceKeepAliveInterval)
	se.SetSTUNGatherTimeout(stunGatherTimeout)
	return pion.NewAPI(pion.WithSettingEngine(se))
}

// Peer wraps a pion RTCPeerConnection together with a single reliable, ordered
// work-dispatch DataChannel. The offerer constructs its channel eagerly via
// CreateDataChannel; the answerer adopts the in-band negotiated channel via
// OnDataChannel. Either way, once the channel reaches the open state, Send and
// the received-envelope queue operate identically on both sides.
type Peer struct {
	pc *pion.PeerConnection

	mu      sync.Mutex
	dc      *pion.DataChannel
	openCh  chan struct{} // closed once the data channel reaches open
	closed  bool
	recvCh  chan WorkEnvelope
	recvErr chan error
}

// NewPeer creates a Peer. If offerer is true, the peer eagerly creates the
// reliable+ordered data channel (it becomes the SDP offerer). If false, the
// peer waits to adopt the channel announced in-band by the remote offerer.
//
// The returned Peer owns the underlying PeerConnection and must be closed with
// Close to release the ICE/DTLS/SCTP resources.
func NewPeer(offerer bool) (*Peer, error) {
	// No ICE servers: loopback host candidates are sufficient for in-process
	// peering. STUN/TURN for real-internet NAT traversal is deferred (HXC-1202).
	// The PeerConnection is built through a hardened API so SCTP message/buffer
	// caps and ICE/DTLS reaping timeouts are enforced from creation.
	pc, err := hardenedAPI().NewPeerConnection(pion.Configuration{})
	if err != nil {
		return nil, fmt.Errorf("webrtc: new peer connection: %w", err)
	}

	p := &Peer{
		pc:      pc,
		openCh:  make(chan struct{}),
		recvCh:  make(chan WorkEnvelope, recvQueueDepth),
		recvErr: make(chan error, 1),
	}

	if offerer {
		ordered := true
		// MaxRetransmits/MaxPacketLifeTime both nil => fully reliable.
		dc, err := pc.CreateDataChannel(DataChannelLabel, &pion.DataChannelInit{
			Ordered: &ordered,
		})
		if err != nil {
			_ = pc.Close()
			return nil, fmt.Errorf("webrtc: create data channel: %w", err)
		}
		p.bindChannel(dc)
	} else {
		pc.OnDataChannel(func(dc *pion.DataChannel) {
			if dc.Label() != DataChannelLabel {
				return
			}
			p.bindChannel(dc)
		})
	}

	return p, nil
}

// bindChannel wires the open/message handlers onto a data channel and records
// it as this peer's active channel.
func (p *Peer) bindChannel(dc *pion.DataChannel) {
	p.mu.Lock()
	p.dc = dc
	p.mu.Unlock()

	dc.OnOpen(func() {
		p.mu.Lock()
		if !p.closed {
			select {
			case <-p.openCh:
				// already closed
			default:
				close(p.openCh)
			}
		}
		p.mu.Unlock()
	})

	dc.OnMessage(func(msg pion.DataChannelMessage) {
		// The pion read callback must never block, so handleInbound is fully
		// non-blocking (bounded select/default sends only). If it reports the
		// frame should close the channel (oversize DoS guard), tear it down off
		// the callback goroutine.
		if closeChannel := p.handleInbound(msg.Data); closeChannel {
			go func() { _ = dc.Close() }()
		}
	})
}

// handleInbound processes one inbound data-channel frame. It is the exact body
// invoked by the data channel's OnMessage handler, factored out so the
// security guards (inbound size cap, bounded non-blocking receive queue) are
// directly exercisable. It MUST NOT block: every channel send is non-blocking.
//
// It returns true iff the caller should close the data channel (used for the
// oversize-frame DoS guard, where the offending peer is dropped rather than
// retried).
func (p *Peer) handleInbound(data []byte) (closeChannel bool) {
	// Inbound size cap (DoS guard): refuse a frame larger than our hard limit
	// BEFORE decoding/allocating envelope fields, so a hostile peer cannot drive
	// OOM with a giant message. Surface an error and signal channel close so the
	// offending peer is dropped rather than retried forever.
	if len(data) > maxInboundMessageBytes {
		select {
		case p.recvErr <- fmt.Errorf("%w: %d bytes", ErrMessageTooLarge, len(data)):
		default:
		}
		return true
	}
	env, err := DecodeEnvelope(data)
	if err != nil {
		select {
		case p.recvErr <- fmt.Errorf("webrtc: decode received envelope: %w", err):
		default:
		}
		return false
	}
	select {
	case p.recvCh <- env:
	default:
		// Receive queue full: surface as an error rather than silently drop
		// (CLAUDE-1: no silent loss of dispatched work) and rather than block the
		// pion read callback (which would stall the whole data channel).
		select {
		case p.recvErr <- errors.New("webrtc: receive queue overflow"):
		default:
		}
	}
	return false
}

// PeerConnection exposes the underlying pion PeerConnection for signaling
// (offer/answer/ICE) and state inspection.
func (p *Peer) PeerConnection() *pion.PeerConnection {
	return p.pc
}

// WaitOpen blocks until the data channel reaches the open state or ctx is done.
// It returns nil once open.
func (p *Peer) WaitOpen(ctx context.Context) error {
	select {
	case <-p.openCh:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("webrtc: wait for data channel open: %w", ctx.Err())
	}
}

// IsOpen reports whether the data channel has reached the open state.
func (p *Peer) IsOpen() bool {
	p.mu.Lock()
	dc := p.dc
	p.mu.Unlock()
	return dc != nil && dc.ReadyState() == pion.DataChannelStateOpen
}

// Ordered reports whether the active data channel is configured for ordered
// delivery. It is only meaningful once the channel exists.
func (p *Peer) Ordered() bool {
	p.mu.Lock()
	dc := p.dc
	p.mu.Unlock()
	return dc != nil && dc.Ordered()
}

// MaxRetransmits returns the channel's MaxRetransmits setting (nil == unlimited
// retransmissions, i.e. fully reliable). Only meaningful once the channel
// exists.
func (p *Peer) MaxRetransmits() *uint16 {
	p.mu.Lock()
	dc := p.dc
	p.mu.Unlock()
	if dc == nil {
		return nil
	}
	return dc.MaxRetransmits()
}

// Send encodes and transmits a WorkEnvelope over the data channel. It returns
// ErrChannelNotOpen if the channel is not yet open.
func (p *Peer) Send(env WorkEnvelope) error {
	p.mu.Lock()
	dc := p.dc
	p.mu.Unlock()
	if dc == nil || dc.ReadyState() != pion.DataChannelStateOpen {
		return ErrChannelNotOpen
	}
	b, err := EncodeEnvelope(env)
	if err != nil {
		return fmt.Errorf("webrtc: encode envelope for send: %w", err)
	}
	if err := dc.Send(b); err != nil {
		return fmt.Errorf("webrtc: send on data channel: %w", err)
	}
	return nil
}

// Receive blocks until a WorkEnvelope is received over the data channel, a
// decode/queue error occurs, or ctx is done.
func (p *Peer) Receive(ctx context.Context) (WorkEnvelope, error) {
	select {
	case env := <-p.recvCh:
		return env, nil
	case err := <-p.recvErr:
		return WorkEnvelope{}, err
	case <-ctx.Done():
		return WorkEnvelope{}, fmt.Errorf("webrtc: receive: %w", ctx.Err())
	}
}

// Close tears down the data channel and peer connection.
func (p *Peer) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	return p.pc.Close()
}
