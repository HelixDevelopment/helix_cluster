package helixnet

import (
	"sync"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/dst"
)

const simEventKind = "helixnet.deliver"

// simDelivery is the payload stored inside a dst.Event for helixnet messages.
type simDelivery struct {
	to      string
	payload []byte
	from    string
}

// simFabric is shared among all SimNetwork instances that use the same *dst.Engine.
// It buffers delivered messages per destination address.
type simFabric struct {
	mu      sync.Mutex
	buffers map[string][]envelope // addr -> ordered list of received envelopes
}

func newSimFabric() *simFabric {
	return &simFabric{buffers: make(map[string][]envelope)}
}

func (f *simFabric) append(to string, env envelope) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buffers[to] = append(f.buffers[to], env)
}

// pop removes and returns the oldest message for addr, or (zero, false) if empty.
func (f *simFabric) pop(addr string) (envelope, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := f.buffers[addr]
	if len(q) == 0 {
		return envelope{}, false
	}
	env := q[0]
	f.buffers[addr] = q[1:]
	return env, true
}

func (f *simFabric) deregister(addr string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.buffers, addr)
}

// engineFabrics maps *dst.Engine -> *simFabric so that multiple SimNetworks
// built on the same engine share one delivery fabric.
var (
	engineFabricsmu sync.Mutex
	engineFabrics   = make(map[*dst.Engine]*simFabric)
)

func simFabricFor(eng *dst.Engine) *simFabric {
	engineFabricsmu.Lock()
	defer engineFabricsmu.Unlock()
	if f, ok := engineFabrics[eng]; ok {
		return f
	}
	f := newSimFabric()
	engineFabrics[eng] = f
	// Register the delivery handler on the engine once per engine.
	eng.RegisterHandler(simEventKind, func(e *dst.Event) {
		d, ok := e.Payload.(*simDelivery)
		if !ok {
			return
		}
		// Clone payload before buffering.
		clone := make([]byte, len(d.payload))
		copy(clone, d.payload)
		f.append(d.to, envelope{payload: clone, from: d.from})
	})
	return f
}

// RemoveSimFabric removes the sim fabric registration for the given engine.
// Call this after a test to prevent cross-test leakage.
func RemoveSimFabric(eng *dst.Engine) {
	engineFabricsmu.Lock()
	defer engineFabricsmu.Unlock()
	delete(engineFabrics, eng)
	// Unregister the handler so a fresh fabric can be registered on next use.
	eng.RegisterHandler(simEventKind, nil)
}

// SimNetwork is a deterministic HelixNetwork backed by a *dst.Engine.
// Messages are scheduled as engine events; after eng.Run() they appear in
// the destination's Receive buffer.  Same seed => same delivery order.
type SimNetwork struct {
	eng    *dst.Engine
	fabric *simFabric
	addr   string

	closedMu sync.Mutex
	isClosed bool
}

// NewSimNetwork creates a SimNetwork bound to localAddr.  The supplied
// *dst.Engine drives event delivery; all SimNetworks sharing the same engine
// share one delivery fabric.
func NewSimNetwork(eng *dst.Engine, localAddr string) (*SimNetwork, error) {
	f := simFabricFor(eng)
	// Ensure we have a buffer slot (even if empty) for this address.
	f.mu.Lock()
	if _, exists := f.buffers[localAddr]; !exists {
		f.buffers[localAddr] = nil
	}
	f.mu.Unlock()

	return &SimNetwork{
		eng:    eng,
		fabric: f,
		addr:   localAddr,
	}, nil
}

// LocalAddr implements HelixNetwork.
func (s *SimNetwork) LocalAddr() string { return s.addr }

// Send implements HelixNetwork by scheduling a delivery event on the engine.
// The payload is cloned so the caller may reuse its buffer immediately.
func (s *SimNetwork) Send(dstAddr string, msg []byte) error {
	s.closedMu.Lock()
	if s.isClosed {
		s.closedMu.Unlock()
		return ErrClosed
	}
	s.closedMu.Unlock()

	clone := make([]byte, len(msg))
	copy(clone, msg)

	d := &simDelivery{to: dstAddr, payload: clone, from: s.addr}
	// Schedule delivery with a virtual delay of 1 ns (preserves order by
	// arrival time; ties are broken by insertion order in the heap).
	s.eng.ScheduleEvent(simEventKind, d, 1)
	return nil
}

// Receive implements HelixNetwork.  In sim mode the caller must drive
// eng.Run(...) before Receive will see messages.  Receive polls the buffer;
// if empty and timeout > 0 it retries a small number of times.  In tests,
// call eng.Run() before Receive.
func (s *SimNetwork) Receive(timeout time.Duration) ([]byte, string, error) {
	s.closedMu.Lock()
	if s.isClosed {
		s.closedMu.Unlock()
		return nil, "", ErrClosed
	}
	s.closedMu.Unlock()

	// Fast path: message already buffered.
	if env, ok := s.fabric.pop(s.addr); ok {
		return env.payload, env.from, nil
	}

	if timeout <= 0 {
		return nil, "", ErrTimeout
	}

	// In simulation tests the caller drives the engine; we do a best-effort
	// spin for up to timeout wall-time, checking the buffer every 100 µs.
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if env, ok := s.fabric.pop(s.addr); ok {
			return env.payload, env.from, nil
		}
		time.Sleep(100 * time.Microsecond)
	}
	return nil, "", ErrTimeout
}

// Close implements HelixNetwork.
func (s *SimNetwork) Close() error {
	s.closedMu.Lock()
	defer s.closedMu.Unlock()
	if s.isClosed {
		return nil
	}
	s.isClosed = true
	s.fabric.deregister(s.addr)
	return nil
}
