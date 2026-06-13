// Package netfault provides a reusable, host-provable TCP fault-injection
// harness for testing the resilience of edge network connections.
//
// The central type is FaultProxy: a real TCP proxy that listens on a local
// port, forwards every accepted connection to a target backend over a real
// socket, and injects configurable, deterministic faults on the bytes that
// flow through it — added latency, bandwidth throttle, partial/whole packet
// drop, connection cut (partition), and reconnect.
//
// Faults are toggleable at runtime through a small control API (SetLatency,
// Partition, Heal, SetThrottle, SetDropRate, ...), so edge components such as
// MQTT / WebSocket / QUIC-over-TCP clients can be pointed at the proxy and
// exercised under network chaos without any external tooling, root, or
// kernel-level packet manipulation. Everything operates on ordinary
// user-space sockets and is therefore provable on any host OS (CLAUDE-2
// cross-platform parity) with real, measured effects (CLAUDE-1 no-bluff).
//
// Real SBC/Android on-device chaos (radio toggling, physical link loss) is
// device-gated and deferred; this harness proves the fault-injection model
// and its observable effects on real loopback sockets.
package netfault

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"sync"
	"time"
)

// ErrProxyClosed is returned by Start when the proxy has already been closed.
var ErrProxyClosed = errors.New("netfault: proxy closed")

// FaultConfig is the initial, declarative fault configuration for a FaultProxy.
// Every field has a safe zero value (no fault), and every field has a runtime
// setter so faults can be toggled while the proxy is live.
type FaultConfig struct {
	// Latency is extra delay added in EACH direction before bytes are
	// forwarded. A round trip therefore observes roughly 2*Latency of added
	// delay (request leg + response leg). Zero means no added latency.
	Latency time.Duration

	// ThrottleBytesPerSec, when > 0, caps the forwarding throughput in each
	// direction to approximately this many bytes per second. Zero means no
	// throttle (line speed).
	ThrottleBytesPerSec int

	// DropRate is the probability in [0,1] that an individual forwarded chunk
	// is silently dropped (data loss without closing the connection). Zero
	// means no drops. Use with care: dropping bytes on a stream protocol
	// corrupts the stream by design — it is meant to exercise client framing
	// and timeout handling.
	DropRate float64

	// Partitioned, when true, starts the proxy in a partitioned (cut) state:
	// new connections are immediately refused/closed and existing ones are
	// severed, simulating a network partition until Heal is called.
	Partitioned bool

	// Rand, if set, is used as the source of randomness for DropRate decisions
	// so tests can be made deterministic. If nil, a private, time-seeded
	// source is used.
	Rand *rand.Rand
}

// FaultProxy is a runtime-controllable TCP fault-injection proxy.
//
// A FaultProxy is safe for concurrent use: all control methods may be called
// from any goroutine while connections are in flight.
type FaultProxy struct {
	target string

	mu                  sync.RWMutex
	latency             time.Duration
	throttleBytesPerSec int
	dropRate            float64
	partitioned         bool
	rng                 *rand.Rand

	listener net.Listener

	closeOnce sync.Once
	closed    chan struct{}

	// conns tracks live forwarded connections so Partition can sever them and
	// Close can drain them.
	connMu sync.Mutex
	conns  map[*forwardedConn]struct{}

	wg sync.WaitGroup
}

// forwardedConn bundles the two real sockets of one proxied connection so a
// partition (or Close) can tear both ends down deterministically.
type forwardedConn struct {
	client net.Conn
	server net.Conn
}

// New creates a FaultProxy that will forward connections to target (a
// host:port string, e.g. "127.0.0.1:1883"). The proxy does not listen until
// Start is called. cfg supplies the initial fault configuration.
func New(target string, cfg FaultConfig) *FaultProxy {
	rng := cfg.Rand
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return &FaultProxy{
		target:              target,
		latency:             cfg.Latency,
		throttleBytesPerSec: cfg.ThrottleBytesPerSec,
		dropRate:            cfg.DropRate,
		partitioned:         cfg.Partitioned,
		rng:                 rng,
		closed:              make(chan struct{}),
		conns:               make(map[*forwardedConn]struct{}),
	}
}

// Start binds a listener on listenAddr (e.g. "127.0.0.1:0" to pick a free
// port) and begins accepting and forwarding connections in the background.
// The bound address is available via Addr after Start returns nil.
func (p *FaultProxy) Start(listenAddr string) error {
	select {
	case <-p.closed:
		return ErrProxyClosed
	default:
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("netfault: listen on %q: %w", listenAddr, err)
	}

	p.mu.Lock()
	p.listener = ln
	p.mu.Unlock()

	p.wg.Add(1)
	go p.acceptLoop(ln)
	return nil
}

// Addr returns the proxy's bound listen address, or nil if not started.
func (p *FaultProxy) Addr() net.Addr {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.listener == nil {
		return nil
	}
	return p.listener.Addr()
}

func (p *FaultProxy) acceptLoop(ln net.Listener) {
	defer p.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			// Listener closed (Close) or transient error; stop on close.
			select {
			case <-p.closed:
				return
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.handle(conn)
		}()
	}
}

// handle services a single accepted client connection: if partitioned it cuts
// immediately, otherwise it dials the backend and pumps bytes both ways with
// the configured faults applied.
func (p *FaultProxy) handle(client net.Conn) {
	if p.isPartitioned() {
		// Partitioned: refuse service by closing the client side. A real
		// connectivity loss — the client observes EOF / connection reset.
		_ = client.Close()
		return
	}

	server, err := net.DialTimeout("tcp", p.target, 5*time.Second)
	if err != nil {
		_ = client.Close()
		return
	}

	fc := &forwardedConn{client: client, server: server}
	p.trackConn(fc)
	defer p.untrackConn(fc)
	defer client.Close()
	defer server.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	// client -> server (request leg)
	go func() {
		defer wg.Done()
		p.pump(server, client)
		// Half-close so the peer sees EOF on this direction.
		if cw, ok := server.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	// server -> client (response leg)
	go func() {
		defer wg.Done()
		p.pump(client, server)
		if cw, ok := client.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	wg.Wait()
}

// pump copies from src to dst applying the live fault configuration to every
// chunk: added latency, throttle, and probabilistic drop. It returns when src
// hits EOF/error, when dst write fails, or when the proxy is closed/partitioned.
func (p *FaultProxy) pump(dst, src net.Conn) {
	buf := make([]byte, 32*1024)
	for {
		select {
		case <-p.closed:
			return
		default:
		}
		// Partition mid-stream: stop pumping so both ends tear down.
		if p.isPartitioned() {
			return
		}

		// Bounded read deadline so a partition toggled while blocked in Read
		// is observed promptly (and so Close drains in bounded time).
		_ = src.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, rerr := src.Read(buf)
		if n > 0 {
			chunk := buf[:n]

			// Added latency (per direction) — real wall-clock delay.
			if lat := p.getLatency(); lat > 0 {
				if !p.sleep(lat) {
					return
				}
			}

			// Bandwidth throttle — pace the chunk to the configured rate.
			if rate := p.getThrottle(); rate > 0 {
				delay := time.Duration(float64(n) / float64(rate) * float64(time.Second))
				if delay > 0 && !p.sleep(delay) {
					return
				}
			}

			// Probabilistic drop — silently discard this chunk.
			if p.shouldDrop() {
				continue
			}

			if _, werr := dst.Write(chunk); werr != nil {
				return
			}
		}
		if rerr != nil {
			if ne, ok := rerr.(net.Error); ok && ne.Timeout() {
				// Deadline we set ourselves: loop to re-check control state.
				continue
			}
			if rerr == io.EOF {
				return
			}
			return
		}
	}
}

// sleep waits for d but returns false early if the proxy is closed.
func (p *FaultProxy) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-p.closed:
		return false
	}
}

// --- runtime control API -------------------------------------------------

// SetLatency sets the per-direction added latency at runtime. A value of 0
// removes the latency fault. Takes effect on the next forwarded chunk.
func (p *FaultProxy) SetLatency(d time.Duration) {
	p.mu.Lock()
	p.latency = d
	p.mu.Unlock()
}

// SetThrottle sets the per-direction throughput cap in bytes/sec at runtime.
// A value of 0 removes the throttle.
func (p *FaultProxy) SetThrottle(bytesPerSec int) {
	p.mu.Lock()
	p.throttleBytesPerSec = bytesPerSec
	p.mu.Unlock()
}

// SetDropRate sets the per-chunk drop probability in [0,1] at runtime. A value
// of 0 removes the drop fault. Values are clamped to [0,1].
func (p *FaultProxy) SetDropRate(rate float64) {
	if rate < 0 {
		rate = 0
	}
	if rate > 1 {
		rate = 1
	}
	p.mu.Lock()
	p.dropRate = rate
	p.mu.Unlock()
}

// Partition cuts the network: all live connections are severed and new
// connections are refused until Heal is called. This is a real connectivity
// loss — clients observe connection reset / EOF and dials fail.
func (p *FaultProxy) Partition() {
	p.mu.Lock()
	p.partitioned = true
	p.mu.Unlock()
	p.severAll()
}

// Heal ends a partition: new connections are accepted and forwarded again.
// (Connections cut by Partition are gone; the client must reconnect — which is
// exactly the reconnect path under test.)
func (p *FaultProxy) Heal() {
	p.mu.Lock()
	p.partitioned = false
	p.mu.Unlock()
}

// IsPartitioned reports whether the proxy is currently partitioned.
func (p *FaultProxy) IsPartitioned() bool { return p.isPartitioned() }

// Close stops accepting, severs all live connections, and waits for the
// background goroutines to drain. It is idempotent.
func (p *FaultProxy) Close() error {
	p.closeOnce.Do(func() {
		close(p.closed)
		p.mu.RLock()
		ln := p.listener
		p.mu.RUnlock()
		if ln != nil {
			_ = ln.Close()
		}
		p.severAll()
	})
	p.wg.Wait()
	return nil
}

// --- internal state helpers ---------------------------------------------

func (p *FaultProxy) getLatency() time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.latency
}

func (p *FaultProxy) getThrottle() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.throttleBytesPerSec
}

func (p *FaultProxy) isPartitioned() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.partitioned
}

func (p *FaultProxy) shouldDrop() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dropRate <= 0 {
		return false
	}
	return p.rng.Float64() < p.dropRate
}

func (p *FaultProxy) trackConn(fc *forwardedConn) {
	p.connMu.Lock()
	p.conns[fc] = struct{}{}
	p.connMu.Unlock()
}

func (p *FaultProxy) untrackConn(fc *forwardedConn) {
	p.connMu.Lock()
	delete(p.conns, fc)
	p.connMu.Unlock()
}

// severAll force-closes both sockets of every tracked connection.
func (p *FaultProxy) severAll() {
	p.connMu.Lock()
	conns := make([]*forwardedConn, 0, len(p.conns))
	for fc := range p.conns {
		conns = append(conns, fc)
	}
	p.connMu.Unlock()
	for _, fc := range conns {
		if fc.client != nil {
			_ = fc.client.Close()
		}
		if fc.server != nil {
			_ = fc.server.Close()
		}
	}
}

// --- convenience constructors -------------------------------------------

// StartLoopback is a convenience that creates a proxy for target and starts it
// on an ephemeral loopback port (127.0.0.1:0). It returns the proxy and the
// "host:port" address clients should dial. The caller owns Close.
func StartLoopback(target string, cfg FaultConfig) (*FaultProxy, string, error) {
	p := New(target, cfg)
	if err := p.Start("127.0.0.1:0"); err != nil {
		return nil, "", err
	}
	return p, p.Addr().String(), nil
}

// Dial dials the proxy's own listen address with the given context. It is a
// thin helper so callers can connect through the proxy without re-deriving the
// address.
func (p *FaultProxy) Dial(ctx context.Context) (net.Conn, error) {
	addr := p.Addr()
	if addr == nil {
		return nil, ErrProxyClosed
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr.String())
}
