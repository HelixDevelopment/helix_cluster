package wireguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// ErrUnsupported is returned by NAT-traversal operations that are not yet
// implemented on this platform. Callers may branch on it with errors.Is.
var ErrUnsupported = errors.New("operation not supported on this platform")

// NATTraversal handles NAT endpoint discovery for WireGuard.
type NATTraversal struct {
	enabled bool
	gateway net.IP
}

// DiscoverExternalAddress attempts to discover the external IP:port for the
// given local port WITHOUT a configured STUN server.
//
// Endpoint discovery requires reaching out to a reflexive server (STUN). With
// no server supplied there is nothing to query, so this returns an honest
// error. Callers that have a STUN server should use
// DiscoverExternalAddressVia, which performs a real RFC 5389 Binding exchange.
func DiscoverExternalAddress(localPort int) (string, error) {
	return "", fmt.Errorf("external address discovery requires a STUN server: use DiscoverExternalAddressVia")
}

// DiscoverExternalAddressVia discovers the public (server-reflexive) endpoint
// by performing a real RFC 5389 STUN Binding exchange against stunServer
// (host:port) over UDP. The returned string is the public "ip:port" reported
// by the server's XOR-MAPPED-ADDRESS attribute.
//
// localPort, when > 0, is the UDP port the discovery socket binds to so that
// the reflexive mapping corresponds to the WireGuard listen port.
func DiscoverExternalAddressVia(stunServer string, localPort int, timeout time.Duration) (string, error) {
	if stunServer == "" {
		return "", fmt.Errorf("stun server address is empty")
	}
	raddr, err := net.ResolveUDPAddr("udp", stunServer)
	if err != nil {
		return "", fmt.Errorf("invalid stun server address %q: %w", stunServer, err)
	}

	var laddr *net.UDPAddr
	if localPort > 0 {
		laddr = &net.UDPAddr{Port: localPort}
	}
	conn, err := net.DialUDP("udp", laddr, raddr)
	if err != nil {
		return "", fmt.Errorf("failed to dial stun server: %w", err)
	}
	defer conn.Close()

	client := &STUNClient{Timeout: timeout}
	addr, err := client.exchange(conn)
	if err != nil {
		return "", fmt.Errorf("stun discovery failed: %w", err)
	}
	return addr.String(), nil
}

// SetupPortMapping creates a UPnP/NAT-PMP port mapping.
//
// UPnP/NAT-PMP is not yet implemented; the function always returns an error
// wrapping ErrUnsupported so callers can branch with errors.Is.
func SetupPortMapping(ctx context.Context, internalPort, externalPort int, duration time.Duration) error {
	// Placeholder for UPnP/NAT-PMP integration.
	// In a full implementation, use github.com/huin/goupnp or similar.
	return fmt.Errorf("UPnP/NAT-PMP: %w", ErrUnsupported)
}

// RemovePortMapping removes a port mapping.
//
// UPnP/NAT-PMP is not yet implemented; the function always returns an error
// wrapping ErrUnsupported so callers can branch with errors.Is.
func RemovePortMapping(ctx context.Context, externalPort int) error {
	// Placeholder for UPnP/NAT-PMP integration.
	return fmt.Errorf("UPnP/NAT-PMP: %w", ErrUnsupported)
}
