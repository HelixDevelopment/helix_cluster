package netfault

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// echoServer stands up a REAL TCP listener that echoes every byte it receives
// back to the sender. It is the backend the FaultProxy forwards to.
type echoServer struct {
	ln   net.Listener
	wg   sync.WaitGroup
	once sync.Once
}

func newEchoServer(t *testing.T) *echoServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	s := &echoServer{ln: ln}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.wg.Add(1)
			go func(c net.Conn) {
				defer s.wg.Done()
				defer c.Close()
				_, _ = io.Copy(c, c) // echo
			}(c)
		}
	}()
	return s
}

func (s *echoServer) addr() string { return s.ln.Addr().String() }

func (s *echoServer) Close() {
	s.once.Do(func() { _ = s.ln.Close() })
	s.wg.Wait()
}

// roundTrip dials addr, writes payload, reads len(payload) bytes back, and
// returns the response together with the measured wall-clock duration.
func roundTrip(t *testing.T, addr string, payload []byte, timeout time.Duration) ([]byte, time.Duration, error) {
	t.Helper()
	d := net.Dialer{Timeout: timeout}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	start := time.Now()
	if _, err := conn.Write(payload); err != nil {
		return nil, time.Since(start), err
	}
	resp := make([]byte, len(payload))
	_, err = io.ReadFull(conn, resp)
	elapsed := time.Since(start)
	return resp, elapsed, err
}

// --- 1a. Baseline: byte-exact pass-through -------------------------------

func TestBaseline_ByteExact(t *testing.T) {
	echo := newEchoServer(t)
	defer echo.Close()

	proxy, addr, err := StartLoopback(echo.addr(), FaultConfig{})
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	payload := []byte("HELIX-EDGE-CHAOS-baseline-0123456789")
	resp, elapsed, err := roundTrip(t, addr, payload, 3*time.Second)
	if err != nil {
		t.Fatalf("baseline round trip: %v", err)
	}
	if !bytes.Equal(resp, payload) {
		t.Fatalf("baseline not byte-exact: got %q want %q", resp, payload)
	}
	t.Logf("BASELINE: byte-exact OK, round-trip=%v", elapsed)
}

// --- 1b. Latency injection: real measured delay --------------------------

func TestLatencyInjection_RealDelay(t *testing.T) {
	echo := newEchoServer(t)
	defer echo.Close()

	proxy, addr, err := StartLoopback(echo.addr(), FaultConfig{})
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	payload := []byte("ping")

	// Baseline (no latency).
	_, base, err := roundTrip(t, addr, payload, 3*time.Second)
	if err != nil {
		t.Fatalf("baseline rt: %v", err)
	}

	// Inject 100ms per-direction latency. A round trip crosses the proxy
	// twice (request + response leg), so the added delay is ~200ms; we assert
	// conservatively that it is at least ~100ms higher than baseline.
	const inject = 100 * time.Millisecond
	proxy.SetLatency(inject)

	_, withLat, err := roundTrip(t, addr, payload, 5*time.Second)
	if err != nil {
		t.Fatalf("latency rt: %v", err)
	}

	delta := withLat - base
	t.Logf("LATENCY: baseline=%v injected(per-dir)=%v measured=%v delta=%v", base, inject, withLat, delta)
	if delta < inject-(20*time.Millisecond) {
		t.Fatalf("latency not genuinely injected: delta=%v < ~%v", delta, inject)
	}
}

// --- 1c. Partition then Heal: real connectivity loss + recovery ----------

func TestPartitionAndHeal(t *testing.T) {
	echo := newEchoServer(t)
	defer echo.Close()

	proxy, addr, err := StartLoopback(echo.addr(), FaultConfig{})
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	payload := []byte("connectivity-probe")

	// Healthy first.
	if resp, _, err := roundTrip(t, addr, payload, 2*time.Second); err != nil || !bytes.Equal(resp, payload) {
		t.Fatalf("pre-partition should succeed: resp=%q err=%v", resp, err)
	}
	t.Logf("PARTITION: pre-cut request SUCCEEDED")

	// Cut the network.
	proxy.Partition()
	if !proxy.IsPartitioned() {
		t.Fatalf("proxy should report partitioned")
	}

	// Now a request must fail (connection refused-equivalent: dial succeeds to
	// the proxy but the proxy closes it; or read fails / times out).
	_, _, err = roundTrip(t, addr, payload, 1500*time.Millisecond)
	if err == nil {
		t.Fatalf("PARTITION FAILED: request succeeded while partitioned (connection was NOT really cut)")
	}
	t.Logf("PARTITION: during-cut request FAILED as required: %v", err)

	// Heal and prove recovery.
	proxy.Heal()
	if proxy.IsPartitioned() {
		t.Fatalf("proxy should report healed")
	}

	// Allow a brief moment for in-flight cut state to settle, then reconnect.
	var healed bool
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, _, rerr := roundTrip(t, addr, payload, 1*time.Second)
		if rerr == nil && bytes.Equal(resp, payload) {
			healed = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !healed {
		t.Fatalf("HEAL FAILED: request did not recover after Heal()")
	}
	t.Logf("HEAL: post-heal request SUCCEEDED (recovery proven)")
}

// --- 1d. Throttle: observable slowdown on a larger transfer --------------

func TestThrottle_ObservableEffect(t *testing.T) {
	echo := newEchoServer(t)
	defer echo.Close()

	proxy, addr, err := StartLoopback(echo.addr(), FaultConfig{})
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	// 64 KiB payload. Unthrottled over loopback this is near-instant.
	payload := bytes.Repeat([]byte("x"), 64*1024)

	_, base, err := roundTrip(t, addr, payload, 5*time.Second)
	if err != nil {
		t.Fatalf("baseline throughput rt: %v", err)
	}

	// Throttle to 64 KiB/s per direction -> ~1s per leg.
	proxy.SetThrottle(64 * 1024)
	_, throttled, err := roundTrip(t, addr, payload, 10*time.Second)
	if err != nil {
		t.Fatalf("throttled rt: %v", err)
	}

	t.Logf("THROTTLE: baseline=%v throttled=%v", base, throttled)
	if throttled < base+500*time.Millisecond {
		t.Fatalf("throttle had no observable effect: baseline=%v throttled=%v", base, throttled)
	}
}

// --- 1d. Drop: observable effect (stream stalls / framing breaks) --------

func TestDrop_ObservableEffect(t *testing.T) {
	echo := newEchoServer(t)
	defer echo.Close()

	proxy, addr, err := StartLoopback(echo.addr(), FaultConfig{})
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	// Drop 100% of chunks: the echo can never complete, so ReadFull must fail
	// (timeout) — an unambiguous, observable data-loss effect.
	proxy.SetDropRate(1.0)
	payload := []byte("this-will-be-dropped")
	_, _, err = roundTrip(t, addr, payload, 1*time.Second)
	if err == nil {
		t.Fatalf("DROP: full drop should prevent echo completion, but round trip succeeded")
	}
	t.Logf("DROP: 100%% drop caused round-trip failure as required: %v", err)
}

// --- 2. Composes with a real protocol: net/http over the proxy -----------

func TestHTTP_LatencyThenPartition(t *testing.T) {
	// Real HTTP backend.
	backend := &http.Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "pong")
	})
	backend.Handler = mux

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("http listen: %v", err)
	}
	go func() { _ = backend.Serve(ln) }()
	defer backend.Close()

	proxy, addr, err := StartLoopback(ln.Addr().String(), FaultConfig{})
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	baseURL := "http://" + addr

	// A fresh client per request avoids keep-alive reuse masking the faults.
	doGet := func(timeout time.Duration) (string, time.Duration, error) {
		client := &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{DisableKeepAlives: true},
		}
		start := time.Now()
		resp, err := client.Get(baseURL + "/ping")
		if err != nil {
			return "", time.Since(start), err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body), time.Since(start), nil
	}

	// Healthy HTTP through the proxy.
	body, base, err := doGet(3 * time.Second)
	if err != nil || body != "pong" {
		t.Fatalf("baseline HTTP: body=%q err=%v", body, err)
	}
	t.Logf("HTTP: baseline GET /ping -> %q in %v", body, base)

	// Inject latency; the HTTP request must visibly take longer.
	const inject = 100 * time.Millisecond
	proxy.SetLatency(inject)
	body, withLat, err := doGet(5 * time.Second)
	if err != nil || body != "pong" {
		t.Fatalf("latency HTTP: body=%q err=%v", body, err)
	}
	delta := withLat - base
	t.Logf("HTTP: with %v injected latency GET took %v (delta=%v)", inject, withLat, delta)
	if delta < inject-(20*time.Millisecond) {
		t.Fatalf("HTTP latency not injected: delta=%v", delta)
	}

	// Heal latency, then partition: the HTTP request must fail.
	proxy.SetLatency(0)
	proxy.Partition()
	_, _, err = doGet(1500 * time.Millisecond)
	if err == nil {
		t.Fatalf("HTTP under partition should FAIL but succeeded (conn not cut)")
	}
	t.Logf("HTTP: GET under partition FAILED as required: %v", err)

	// Heal and prove the HTTP request recovers.
	proxy.Heal()
	var ok bool
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, _, e := doGet(1 * time.Second); e == nil && b == "pong" {
			ok = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ok {
		t.Fatalf("HTTP did not recover after Heal()")
	}
	t.Logf("HTTP: GET recovered after Heal()")
}

// --- helper sanity: a line-based exchange through the proxy --------------

func TestLineExchange(t *testing.T) {
	// Backend that upper-cases each line — proves bidirectional integrity
	// through the proxy, not just echo.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if len(line) > 0 {
						_, _ = c.Write(bytes.ToUpper([]byte(line)))
					}
					if err != nil {
						return
					}
				}
			}(c)
		}
	}()

	proxy, addr, err := StartLoopback(ln.Addr().String(), FaultConfig{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer proxy.Close()

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("hello-edge\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "HELLO-EDGE\n" {
		t.Fatalf("line exchange wrong: %q", got)
	}
	t.Logf("LINE: bidirectional exchange OK -> %q", got)
}
