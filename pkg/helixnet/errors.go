package helixnet

import "errors"

// Sentinel errors returned by HelixNetwork implementations.
var (
	// ErrTimeout is returned by Receive when no message arrives within the deadline.
	ErrTimeout = errors.New("helixnet: receive timeout")

	// ErrClosed is returned when an operation is attempted on a closed endpoint.
	ErrClosed = errors.New("helixnet: endpoint closed")

	// ErrUnknownAddr is returned by Send when the destination address is not registered.
	ErrUnknownAddr = errors.New("helixnet: unknown destination address")
)
