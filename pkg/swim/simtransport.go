package swim

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"sort"
	"sync"
	"time"
)

// MessageTransport is the send/receive seam the SWIM core uses for probes and
// gossip. The production *Transport (real UDP) satisfies it, and so does
// *SimTransport (in-memory, deterministic, virtual clock) used for
// deterministic-simulation testing (DST). Introducing it as an interface is
// backward-compatible: every existing caller already uses these exact method
// signatures on *Transport.
type MessageTransport interface {
	// LocalAddr returns the transport's local address.
	LocalAddr() net.Addr
	// Send delivers msg to addr. For SimTransport, "delivery" means enqueueing
	// the message on the virtual network's logical event queue subject to the
	// configured drop/delay/reorder policy.
	Send(msg *Message, addr net.UDPAddr) error
	// RegisterHandler registers a handler for a message type.
	RegisterHandler(msgType MessageType, handler func(*Message, net.Addr))
	// Listen blocks processing inbound messages until ctx is cancelled. For
	// SimTransport this is a no-op blocker: delivery is driven synchronously by
	// SimNetwork.Run rather than by a background read loop.
	Listen(ctx context.Context) error
	// Close releases the transport.
	Close() error
}

// Compile-time proof both transports satisfy the seam.
var (
	_ MessageTransport = (*Transport)(nil)
	_ MessageTransport = (*SimTransport)(nil)
)

// SimNetwork is an in-memory, deterministic virtual network fabric that wires N
// SimTransport endpoints together. All non-determinism (drop/delay/reorder
// decisions) is driven by a single seeded *math/rand.Rand, and all timing is
// driven by a logical virtual clock advanced inside Run. Given the same seed,
// the same set of Sends produces the identical delivery schedule and therefore
// the identical sequence of membership events — this is the determinism
// guarantee DST relies on.
//
// SimNetwork is NOT safe for use across real goroutines; the DST model is
// single-threaded by design (the whole point is a reproducible logical
// schedule). Sends from message handlers (which Run invokes synchronously) are
// supported and re-queued onto the same logical queue.
type SimNetwork struct {
	rng *rand.Rand

	// now is the virtual clock in logical nanoseconds since epoch.
	now int64

	// endpoints maps an address string ("127.0.0.1:PORT") to its transport.
	endpoints map[string]*SimTransport

	// seq is a monotonically increasing enqueue counter used as a deterministic
	// tie-breaker so that two messages with the identical delivery time are
	// always ordered by enqueue order (stable, reproducible).
	seq uint64

	// queue is the logical event queue ordered by (deliverAt, seq).
	queue []*simEnvelope

	// policy decides per-message delivery (deliver/drop/delay).
	policy DeliveryPolicy

	// drops counts messages the policy dropped (observability / sink-side
	// assertions in tests).
	drops int

	// delivered counts messages actually handed to a handler.
	delivered int
}

// simEnvelope is one in-flight message on the virtual network.
type simEnvelope struct {
	from      string
	to        string
	msg       *Message
	deliverAt int64
	seq       uint64
}

// DeliveryPolicy decides, deterministically, what happens to a message sent
// from one endpoint to another. It is consulted exactly once per Send. The
// returned delay (>=0) is added to the virtual clock to compute the delivery
// time; returning drop=true discards the message entirely (modelling a lost
// packet or a one-way network partition). Implementations MUST be deterministic
// functions of their inputs (including any rng they pull from SimNetwork) so a
// fixed seed reproduces a fixed schedule.
type DeliveryPolicy func(net *SimNetwork, from, to string, msg *Message) (delay time.Duration, drop bool)

// PerfectLink is the default DeliveryPolicy: never drops, zero delay. Messages
// are still ordered by enqueue sequence, giving FIFO-per-network delivery.
func PerfectLink(_ *SimNetwork, _, _ string, _ *Message) (time.Duration, bool) {
	return 0, false
}

// NewSimNetwork creates a deterministic virtual network seeded by seed. If
// policy is nil, PerfectLink is used.
func NewSimNetwork(seed int64, policy DeliveryPolicy) *SimNetwork {
	if policy == nil {
		policy = PerfectLink
	}
	return &SimNetwork{
		rng:       rand.New(rand.NewSource(seed)),
		endpoints: make(map[string]*SimTransport),
		policy:    policy,
	}
}

// Rand exposes the network's seeded RNG so DeliveryPolicy implementations can
// make reproducible random decisions. Pulling from this source is the ONLY
// sanctioned source of randomness in a DST run.
func (n *SimNetwork) Rand() *rand.Rand { return n.rng }

// Now returns the current virtual time.
func (n *SimNetwork) Now() time.Time { return time.Unix(0, n.now) }

// Drops returns how many messages the policy has dropped so far.
func (n *SimNetwork) Drops() int { return n.drops }

// Delivered returns how many messages have been delivered to a handler so far.
func (n *SimNetwork) Delivered() int { return n.delivered }

// NewTransport creates a SimTransport endpoint bound to addr (a "127.0.0.1:PORT"
// style string so that handler code which round-trips addresses through
// net.ResolveUDPAddr keeps working unchanged) and registers it on the network.
func (n *SimNetwork) NewTransport(addr string) (*SimTransport, error) {
	udp, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("resolve sim addr %q: %w", addr, err)
	}
	canon := udp.String()
	if _, exists := n.endpoints[canon]; exists {
		return nil, fmt.Errorf("sim endpoint %q already exists", canon)
	}
	st := &SimTransport{
		net:      n,
		addr:     udp,
		handlers: make(map[MessageType]func(*Message, net.Addr)),
	}
	n.endpoints[canon] = st
	return st, nil
}

// enqueue is called by SimTransport.Send. It consults the policy, and either
// drops the message or schedules it on the logical queue at now+delay.
func (n *SimNetwork) enqueue(from, to string, msg *Message) {
	delay, drop := n.policy(n, from, to, msg)
	if drop {
		n.drops++
		return
	}
	if delay < 0 {
		delay = 0
	}
	n.seq++
	env := &simEnvelope{
		from:      from,
		to:        to,
		msg:       msg,
		deliverAt: n.now + int64(delay),
		seq:       n.seq,
	}
	n.queue = append(n.queue, env)
}

// deliverNext pops the earliest-deadline envelope (ties broken by enqueue seq),
// advances virtual time to its delivery instant, and invokes the destination
// handler synchronously. It returns false when the queue is empty.
func (n *SimNetwork) deliverNext() bool {
	if len(n.queue) == 0 {
		return false
	}
	sort.SliceStable(n.queue, func(i, j int) bool {
		if n.queue[i].deliverAt == n.queue[j].deliverAt {
			return n.queue[i].seq < n.queue[j].seq
		}
		return n.queue[i].deliverAt < n.queue[j].deliverAt
	})
	env := n.queue[0]
	n.queue = n.queue[1:]
	if env.deliverAt > n.now {
		n.now = env.deliverAt
	}
	dst, ok := n.endpoints[env.to]
	if !ok {
		return true // addressed to an unknown endpoint: black-holed but consumed.
	}
	handler, ok := dst.handlers[env.msg.Type]
	if !ok {
		return true
	}
	n.delivered++
	handler(env.msg, simAddr{addr: env.from})
	return true
}

// Run drains the logical queue, delivering messages in deterministic order,
// until the queue is empty or maxSteps deliveries have occurred (maxSteps<=0
// means unbounded). Because handlers may Send (re-queueing), Run continues
// until quiescence. It returns the number of messages delivered.
func (n *SimNetwork) Run(maxSteps int) int {
	steps := 0
	for {
		if maxSteps > 0 && steps >= maxSteps {
			return steps
		}
		if !n.deliverNext() {
			return steps
		}
		steps++
	}
}

// Advance moves the virtual clock forward by d without delivering anything. This
// lets a DST scenario model the passage of wall time (e.g. probe rounds) so that
// time-scheduled work driven off SimNetwork.Now() observes the advance.
func (n *SimNetwork) Advance(d time.Duration) {
	if d < 0 {
		return
	}
	n.now += int64(d)
}

// simAddr is a net.Addr whose String() is a canonical "127.0.0.1:PORT" so that
// handler code calling net.ResolveUDPAddr("udp", addr.String()) round-trips back
// to the originating SimTransport endpoint.
type simAddr struct{ addr string }

func (a simAddr) Network() string { return "udp" }
func (a simAddr) String() string  { return a.addr }

// SimTransport is an in-memory MessageTransport endpoint on a SimNetwork. It has
// no sockets and no real timers: Send hands the message to the network's logical
// queue, and inbound delivery is driven by SimNetwork.Run.
type SimTransport struct {
	net  *SimNetwork
	addr *net.UDPAddr

	mu       sync.RWMutex
	handlers map[MessageType]func(*Message, net.Addr)
	closed   bool
}

// LocalAddr returns this endpoint's virtual UDP address.
func (s *SimTransport) LocalAddr() net.Addr { return s.addr }

// Send enqueues msg for delivery to addr on the virtual network. It never
// touches a socket. A send to a closed transport is dropped.
func (s *SimTransport) Send(msg *Message, addr net.UDPAddr) error {
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return fmt.Errorf("sim transport %s closed", s.addr)
	}
	// Defensive copy so later mutation by the caller cannot retroactively alter
	// an already-enqueued message (real UDP serialises a snapshot too).
	cp := *msg
	if msg.Payload != nil {
		cp.Payload = append([]byte(nil), msg.Payload...)
	}
	s.net.enqueue(s.addr.String(), addr.String(), &cp)
	return nil
}

// RegisterHandler registers a handler for a message type.
func (s *SimTransport) RegisterHandler(msgType MessageType, handler func(*Message, net.Addr)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[msgType] = handler
}

// Listen blocks until ctx is cancelled. SimTransport has no read loop; delivery
// is driven by SimNetwork.Run, so this merely parks to satisfy the seam.
func (s *SimTransport) Listen(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// Close marks the transport closed; subsequent Sends fail.
func (s *SimTransport) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
