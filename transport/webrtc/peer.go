package webrtc

import (
	"context"
	"errors"
	"fmt"
	"sync"

	pion "github.com/pion/webrtc/v4"
)

// DataChannelLabel is the label of the reliable, ordered work-dispatch data
// channel created between two Helix edge peers.
const DataChannelLabel = "helix-dispatch"

// ErrChannelNotOpen is returned by Send when the data channel has not yet
// reached the open state.
var ErrChannelNotOpen = errors.New("webrtc: data channel not open")

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
	pc, err := pion.NewPeerConnection(pion.Configuration{})
	if err != nil {
		return nil, fmt.Errorf("webrtc: new peer connection: %w", err)
	}

	p := &Peer{
		pc:      pc,
		openCh:  make(chan struct{}),
		recvCh:  make(chan WorkEnvelope, 64),
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
		env, err := DecodeEnvelope(msg.Data)
		if err != nil {
			select {
			case p.recvErr <- fmt.Errorf("webrtc: decode received envelope: %w", err):
			default:
			}
			return
		}
		select {
		case p.recvCh <- env:
		default:
			// Receive queue full: surface as an error rather than silently drop
			// (CLAUDE-1: no silent loss of dispatched work).
			select {
			case p.recvErr <- errors.New("webrtc: receive queue overflow"):
			default:
			}
		}
	})
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
