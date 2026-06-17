package helixnet

import (
	"bytes"
	"sync"
	"time"
)

// envelope is a single message in an endpoint's inbound queue.
type envelope struct {
	payload []byte
	from    string
}

// prodEndpoint is the per-address state inside the shared prod fabric.
type prodEndpoint struct {
	ch     chan envelope
	closed bool
}

// prodFabric is a process-wide registry mapping address -> endpoint.
// Multiple ProdNetworks created with NewProdNetwork share the same fabric
// within the same Go process (or a custom fabric injected via NewProdNetworkWithFabric).
type prodFabric struct {
	mu        sync.RWMutex
	endpoints map[string]*prodEndpoint
}

func newProdFabric() *prodFabric {
	return &prodFabric{endpoints: make(map[string]*prodEndpoint)}
}

// register adds a new endpoint.  Returns an error if the address is already taken.
func (f *prodFabric) register(addr string) (*prodEndpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ep, ok := f.endpoints[addr]; ok && !ep.closed {
		return nil, ErrUnknownAddr // address collision — treat as already used
	}
	ep := &prodEndpoint{ch: make(chan envelope, 256)}
	f.endpoints[addr] = ep
	return ep, nil
}

// deregister marks an endpoint closed, removes it from the fabric, and closes
// its channel.
//
// The close happens HERE, under the fabric write lock, rather than in
// ProdNetwork.Close — this is load-bearing for crash safety. deliver holds the
// fabric READ lock across its channel send (below), so taking the WRITE lock
// here makes the close mutually exclusive with any in-flight send. Without that
// ordering, a deliver that had looked up the endpoint and released the lock
// could send on a channel a concurrent Close had already closed, triggering a
// process-killing "send on closed channel" panic and a data race (the classic
// read-by-many/written-by-few teardown race).
func (f *prodFabric) deregister(addr string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ep, ok := f.endpoints[addr]; ok {
		ep.closed = true
		delete(f.endpoints, addr)
		close(ep.ch)
	}
}

// deliver puts an envelope onto the destination endpoint's channel.
//
// The RLock is held ACROSS the send (not released after the lookup): the send
// is non-blocking (the select has a default branch), so holding the read lock
// during it cannot deadlock, and it guarantees the channel cannot be closed by
// a concurrent deregister (which needs the write lock) while this send is in
// flight. Once deregister has run, the endpoint is gone from the map, so a later
// deliver simply returns ErrUnknownAddr — never a send on a closed channel.
func (f *prodFabric) deliver(dstAddr string, env envelope) error {
	f.mu.RLock()
	defer f.mu.RUnlock()
	ep, ok := f.endpoints[dstAddr]
	if !ok {
		return ErrUnknownAddr
	}
	select {
	case ep.ch <- env:
		return nil
	default:
		// channel full — treat as a transient delivery failure
		return ErrUnknownAddr
	}
}

// DefaultProdFabric is the process-wide default fabric used when
// NewProdNetwork is called without an explicit fabric.
var DefaultProdFabric = newProdFabric()

// ProdNetwork is a real, in-process transport backed by Go channels.
// Multiple ProdNetwork instances sharing the same *prodFabric can exchange
// messages with zero-copy semantics (bytes are cloned on send so callers
// may reuse their buffer safely).
type ProdNetwork struct {
	addr   string
	ep     *prodEndpoint
	fabric *prodFabric

	closedMu sync.Mutex
	isClosed bool
}

// NewProdNetwork creates a ProdNetwork bound to localAddr using the
// DefaultProdFabric.  The address must be unique within the fabric.
func NewProdNetwork(localAddr string) (*ProdNetwork, error) {
	return NewProdNetworkWithFabric(localAddr, DefaultProdFabric)
}

// NewProdNetworkWithFabric creates a ProdNetwork bound to localAddr inside the
// supplied fabric.  This allows tests to create isolated fabrics that do not
// share state with DefaultProdFabric.
func NewProdNetworkWithFabric(localAddr string, fabric *prodFabric) (*ProdNetwork, error) {
	ep, err := fabric.register(localAddr)
	if err != nil {
		return nil, err
	}
	return &ProdNetwork{addr: localAddr, ep: ep, fabric: fabric}, nil
}

// NewProdFabric creates a fresh, isolated prod fabric.
func NewProdFabric() *prodFabric {
	return newProdFabric()
}

// LocalAddr implements HelixNetwork.
func (p *ProdNetwork) LocalAddr() string { return p.addr }

// Send implements HelixNetwork.  The bytes are cloned before queuing so the
// caller may freely mutate its buffer after Send returns.
func (p *ProdNetwork) Send(dstAddr string, msg []byte) error {
	p.closedMu.Lock()
	if p.isClosed {
		p.closedMu.Unlock()
		return ErrClosed
	}
	p.closedMu.Unlock()

	// Clone payload to avoid aliasing.
	clone := make([]byte, len(msg))
	copy(clone, msg)
	return p.fabric.deliver(dstAddr, envelope{payload: clone, from: p.addr})
}

// Receive implements HelixNetwork.  It blocks until a message arrives, timeout
// elapses, or the endpoint is closed.
func (p *ProdNetwork) Receive(timeout time.Duration) ([]byte, string, error) {
	p.closedMu.Lock()
	if p.isClosed {
		p.closedMu.Unlock()
		return nil, "", ErrClosed
	}
	p.closedMu.Unlock()

	// Fast path: if a message is already buffered (or the channel is closed),
	// serve it immediately. Without this, a Receive with a small/zero timeout
	// races a ready buffered message against an already-expired timer in the
	// select below; Go picks uniformly among ready cases, so a buffered message
	// would be spuriously reported as ErrTimeout ~half the time. The interface
	// contract is "blocks until a message arrives OR timeout elapses" — a
	// message that has ALREADY arrived must be delivered, never skipped. (Mirrors
	// SimNetwork.Receive's buffer-first fast path.)
	select {
	case env, ok := <-p.ep.ch:
		if !ok {
			return nil, "", ErrClosed
		}
		return env.payload, env.from, nil
	default:
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case env, ok := <-p.ep.ch:
		if !ok {
			return nil, "", ErrClosed
		}
		return env.payload, env.from, nil
	case <-timer.C:
		return nil, "", ErrTimeout
	}
}

// Close implements HelixNetwork.
func (p *ProdNetwork) Close() error {
	p.closedMu.Lock()
	defer p.closedMu.Unlock()
	if p.isClosed {
		return nil
	}
	p.isClosed = true
	// deregister removes the endpoint AND closes its channel under the fabric
	// write lock, so the close is serialized against any in-flight deliver (which
	// holds the read lock across its send). Closing here instead would race that
	// send and panic with "send on closed channel".
	p.fabric.deregister(p.addr)
	return nil
}

// bytesEqual is a small helper used in tests to compare payloads.
func bytesEqual(a, b []byte) bool { return bytes.Equal(a, b) }
