// Package helixnet defines the HelixNetwork interface — a transport abstraction
// used across Helix Cluster OS.  Payloads are raw []byte; addresses are opaque
// strings so the interface is fully decoupled from any higher-level protocol
// (e.g. pkg/swim).
//
// Two implementations are provided:
//   - ProdNetwork  (prod.go)  — real in-process channel-based transport
//   - SimNetwork   (sim.go)   — deterministic simulation backed by pkg/testing/dst
package helixnet

import "time"

// HelixNetwork is the transport interface every implementation must satisfy.
type HelixNetwork interface {
	// LocalAddr returns the address this endpoint is bound to.
	LocalAddr() string

	// Send transmits msg to the endpoint identified by dstAddr.
	// Returns an error if the destination is unknown or the endpoint is closed.
	Send(dstAddr string, msg []byte) error

	// Receive blocks until a message arrives or timeout elapses.
	// On success it returns (payload, senderAddr, nil).
	// On timeout it returns (nil, "", ErrTimeout).
	// On close it returns (nil, "", ErrClosed).
	Receive(timeout time.Duration) (msg []byte, from string, err error)

	// Close shuts down the endpoint and releases its resources.
	Close() error
}
