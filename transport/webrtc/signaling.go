package webrtc

import (
	"context"
	"fmt"

	pion "github.com/pion/webrtc/v4"
)

// Connect performs a complete in-process WebRTC signaling handshake between two
// local Peers over loopback: it exchanges the SDP offer/answer and trickles ICE
// candidates directly between the two PeerConnections (no external signaling
// server). It returns once both data channels reach the open state, or when ctx
// is done.
//
// offerer must have been created with NewPeer(true) and answerer with
// NewPeer(false). This exercises the real pion ICE/DTLS/SCTP stack end to end.
func Connect(ctx context.Context, offerer, answerer *Peer) error {
	op := offerer.PeerConnection()
	ap := answerer.PeerConnection()

	// Trickle ICE: forward each gathered candidate to the remote peer as soon
	// as it is discovered. A nil candidate marks end-of-gathering and is not
	// forwarded.
	addErr := make(chan error, 8)
	op.OnICECandidate(func(c *pion.ICECandidate) {
		if c == nil {
			return
		}
		if err := ap.AddICECandidate(c.ToJSON()); err != nil {
			select {
			case addErr <- fmt.Errorf("webrtc: add offerer candidate to answerer: %w", err):
			default:
			}
		}
	})
	ap.OnICECandidate(func(c *pion.ICECandidate) {
		if c == nil {
			return
		}
		if err := op.AddICECandidate(c.ToJSON()); err != nil {
			select {
			case addErr <- fmt.Errorf("webrtc: add answerer candidate to offerer: %w", err):
			default:
			}
		}
	})

	// Offer.
	offer, err := op.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("webrtc: create offer: %w", err)
	}
	if err := op.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("webrtc: offerer set local description: %w", err)
	}
	if err := ap.SetRemoteDescription(offer); err != nil {
		return fmt.Errorf("webrtc: answerer set remote description: %w", err)
	}

	// Answer.
	answer, err := ap.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("webrtc: create answer: %w", err)
	}
	if err := ap.SetLocalDescription(answer); err != nil {
		return fmt.Errorf("webrtc: answerer set local description: %w", err)
	}
	if err := op.SetRemoteDescription(answer); err != nil {
		return fmt.Errorf("webrtc: offerer set remote description: %w", err)
	}

	// Wait for both data channels to open (real ICE/DTLS/SCTP completion).
	if err := offerer.WaitOpen(ctx); err != nil {
		return fmt.Errorf("webrtc: offerer data channel: %w", err)
	}
	if err := answerer.WaitOpen(ctx); err != nil {
		return fmt.Errorf("webrtc: answerer data channel: %w", err)
	}

	// Surface any non-fatal candidate-add error observed during the handshake.
	select {
	case err := <-addErr:
		return err
	default:
	}
	return nil
}
