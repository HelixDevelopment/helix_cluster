package swim

import (
	"net"
	"time"
)

// transportProber is the production Prober. It probes members over the real UDP
// transport: a direct probe sends a Ping and waits up to probeTimeout for the
// member's lastSeen timestamp to advance (an ack updates it via handleAck); an
// indirect probe sends Ping-Req to up to k relay members asking them to ping the
// target on this node's behalf, then waits for the target's lastSeen to advance.
//
// It deliberately reuses the existing ack bookkeeping (Member.Touch via
// handleAck) so it does not need to alter the transport's handler registration
// or the wire format — preserving backward compatibility.
type transportProber struct {
	transport    MessageTransport
	localID      string
	probeTimeout time.Duration
	wait         func(time.Duration) // injectable sleep; defaults to time.Sleep
}

// newTransportProber builds a Prober bound to a transport. transport is the
// MessageTransport seam, so this prober works over both the real UDP *Transport
// and the in-memory *SimTransport used in deterministic-simulation tests.
func newTransportProber(t MessageTransport, localID string, probeTimeout time.Duration) *transportProber {
	if probeTimeout <= 0 {
		probeTimeout = 500 * time.Millisecond
	}
	return &transportProber{
		transport:    t,
		localID:      localID,
		probeTimeout: probeTimeout,
		wait:         time.Sleep,
	}
}

// DirectProbe pings target and reports whether it acked within probeTimeout.
func (tp *transportProber) DirectProbe(target *Member) bool {
	addr, err := net.ResolveUDPAddr("udp", target.Address)
	if err != nil {
		return false
	}
	before := target.LastSeen()
	msg := &Message{Type: MsgPing, SourceID: tp.localID, TargetID: target.ID}
	if err := tp.transport.Send(msg, *addr); err != nil {
		return false
	}
	tp.wait(tp.probeTimeout)
	// If an ack arrived, handleAck called Touch(), advancing lastSeen.
	return target.LastSeen().After(before)
}

// IndirectProbe asks each relay to ping target on our behalf and reports whether
// target's lastSeen advanced within probeTimeout (i.e. some relay reached it).
func (tp *transportProber) IndirectProbe(target *Member, relays []*Member) bool {
	if len(relays) == 0 {
		return false
	}
	before := target.LastSeen()
	for _, relay := range relays {
		raddr, err := net.ResolveUDPAddr("udp", relay.Address)
		if err != nil {
			continue
		}
		req := &Message{
			Type:     MsgPingReq,
			SourceID: tp.localID,
			TargetID: target.ID,
			Payload:  []byte(target.Address),
		}
		tp.transport.Send(req, *raddr)
	}
	tp.wait(tp.probeTimeout)
	return target.LastSeen().After(before)
}
