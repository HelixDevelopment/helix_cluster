package turn

import (
	"errors"
	"fmt"
	"net"
	"time"

	pionturn "github.com/pion/turn/v4"
)

// Endpoint describes a single TURN server in the fallback chain together with
// the long-term credentials used to authenticate against it.
//
// Address is the UDP host:port of the TURN server (e.g. "turn.example.com:3478").
// Username/Password/Realm are the RFC 5766 long-term credential mechanism inputs.
// Realm may be left empty; the server's advertised realm is then used by the
// underlying pion client during the second (authenticated) allocation round
// trip.
type Endpoint struct {
	// Address is the UDP host:port of the TURN server.
	Address string
	// Username is the long-term-credential username.
	Username string
	// Password is the long-term-credential password.
	Password string
	// Realm is the long-term-credential realm. Optional; the server-advertised
	// realm is used when empty.
	Realm string
}

func (e Endpoint) validate() error {
	if e.Address == "" {
		return errors.New("turn: endpoint address is empty")
	}
	if e.Username == "" {
		return errors.New("turn: endpoint username is empty")
	}
	if e.Password == "" {
		return errors.New("turn: endpoint password is empty")
	}
	return nil
}

// Config configures a RelayManager.
type Config struct {
	// Endpoints is the ordered TURN server fallback chain. Allocation is
	// attempted against each endpoint in order; the first that grants a relayed
	// address wins. Must contain at least one endpoint.
	Endpoints []Endpoint

	// DialTimeout bounds how long a single endpoint's allocation attempt may
	// take before it is treated as failed and the chain advances to the next
	// endpoint. Defaults to 5s when zero.
	DialTimeout time.Duration
}

// RelayManager allocates relayed transport addresses from an ordered chain of
// TURN servers, falling back to the next server on failure. It is the
// NAT-traversal fallback complementing the ICE/WebRTC transport.
type RelayManager struct {
	endpoints   []Endpoint
	dialTimeout time.Duration
}

// NewRelayManager constructs a RelayManager from cfg. It returns an error if no
// endpoints are configured or any endpoint is missing required credentials.
func NewRelayManager(cfg Config) (*RelayManager, error) {
	if len(cfg.Endpoints) == 0 {
		return nil, errors.New("turn: no endpoints configured")
	}
	for i, ep := range cfg.Endpoints {
		if err := ep.validate(); err != nil {
			return nil, fmt.Errorf("turn: endpoint[%d]: %w", i, err)
		}
	}
	dt := cfg.DialTimeout
	if dt <= 0 {
		dt = 5 * time.Second
	}
	// Copy the slice so later mutation of the caller's slice cannot change the
	// chain after construction.
	eps := make([]Endpoint, len(cfg.Endpoints))
	copy(eps, cfg.Endpoints)
	return &RelayManager{endpoints: eps, dialTimeout: dt}, nil
}

// Allocation is a successfully granted TURN relay.
//
// RelayedAddr is the server-granted relayed transport address other peers send
// to in order to reach the owner of this allocation. RelayConn is the relayed
// net.PacketConn: reading from it yields traffic the server relayed from peers,
// and WriteTo sends through the relay to a peer (a permission must exist first).
// Client is the underlying pion TURN client, exposed so callers can install
// peer permissions (CreatePermission) and channel bindings. Endpoint is the
// chain entry that actually served this allocation.
type Allocation struct {
	// RelayConn is the relayed packet connection. Its LocalAddr() equals
	// RelayedAddr.
	RelayConn net.PacketConn
	// RelayedAddr is the server-granted relayed transport address.
	RelayedAddr net.Addr
	// Client is the underlying pion TURN client backing this allocation.
	Client *pionturn.Client
	// Endpoint is the TURN server that served this allocation.
	Endpoint Endpoint

	// localConn is the UDP socket used to talk to the TURN server; held so
	// Close can release it after the client and relay conn are torn down.
	localConn net.PacketConn
}

// Close releases the allocation: it closes the relayed connection, the TURN
// client, and the local UDP socket bound to reach the server.
func (a *Allocation) Close() error {
	var errs []error
	if a.RelayConn != nil {
		if err := a.RelayConn.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if a.Client != nil {
		a.Client.Close()
	}
	if a.localConn != nil {
		if err := a.localConn.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Allocate walks the endpoint chain in order and returns the first successful
// allocation. On every endpoint failure it records the error and advances to
// the next endpoint. If every endpoint fails, it returns a joined error
// describing each failure in chain order.
//
// THE FALLBACK INVARIANT: a failure on endpoint i must NOT short-circuit the
// chain — endpoints i+1..n MUST still be tried. The anti-bluff mutation in the
// test suite verifies exactly this by making the loop return the first error
// instead of continuing; that mutation makes the fallback test fail.
func (m *RelayManager) Allocate() (*Allocation, error) {
	var errs []error
	for i := range m.endpoints {
		ep := m.endpoints[i]
		alloc, err := m.allocateFrom(ep)
		if err != nil {
			errs = append(errs, fmt.Errorf("turn: endpoint[%d] %q: %w", i, ep.Address, err))
			continue // <-- fallback: try the NEXT endpoint
		}
		return alloc, nil
	}
	return nil, fmt.Errorf("turn: all %d endpoint(s) failed: %w", len(m.endpoints), errors.Join(errs...))
}

// allocateFrom performs a single real TURN allocation against one endpoint.
func (m *RelayManager) allocateFrom(ep Endpoint) (alloc *Allocation, err error) {
	// Local UDP socket used to reach the TURN server.
	localConn, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("listen local udp: %w", err)
	}
	// On any failure past this point, make sure the socket is released.
	defer func() {
		if err != nil {
			_ = localConn.Close()
		}
	}()

	cfg := &pionturn.ClientConfig{
		TURNServerAddr: ep.Address,
		Username:       ep.Username,
		Password:       ep.Password,
		Realm:          ep.Realm,
		Conn:           localConn,
		RTO:            m.dialTimeout,
	}

	client, err := pionturn.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("new client: %w", err)
	}
	defer func() {
		if err != nil {
			client.Close()
		}
	}()

	// Listen() pumps inbound STUN/TURN traffic from the server into the client.
	if err = client.Listen(); err != nil {
		return nil, fmt.Errorf("client listen: %w", err)
	}

	// Allocate() performs the real two-round-trip TURN Allocate against the
	// server: an unauthenticated probe that draws a 401 + realm/nonce, then the
	// authenticated request. A dead/unreachable server times out here; wrong
	// credentials are rejected here.
	relayConn, err := client.Allocate()
	if err != nil {
		return nil, fmt.Errorf("allocate: %w", err)
	}

	relayedAddr := relayConn.LocalAddr()
	if relayedAddr == nil {
		err = errors.New("server granted a nil relayed address")
		_ = relayConn.Close()
		return nil, err
	}

	a := &Allocation{
		RelayConn:   relayConn,
		RelayedAddr: relayedAddr,
		Client:      client,
		Endpoint:    ep,
	}
	a.localConn = localConn
	return a, nil
}
