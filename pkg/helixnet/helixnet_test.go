package helixnet_test

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/helixnet"
	"github.com/HelixDevelopment/helix_cluster/pkg/testing/dst"
)

// ── Compile-time interface assertions ──────────────────────────────────────

var _ helixnet.HelixNetwork = (*helixnet.ProdNetwork)(nil)
var _ helixnet.HelixNetwork = (*helixnet.SimNetwork)(nil)

// ── helpers ────────────────────────────────────────────────────────────────

// uniqueAddr returns a test-scoped address that won't collide with other tests.
func uniqueAddr(t *testing.T, suffix string) string {
	t.Helper()
	return fmt.Sprintf("test-%s-%s", t.Name(), suffix)
}

// newIsolatedProdNet creates a ProdNetwork inside a fresh fabric (no
// DefaultProdFabric pollution across tests).
func newIsolatedProdNet(t *testing.T, addr string) *helixnet.ProdNetwork {
	t.Helper()
	fabric := helixnet.NewProdFabric()
	n, err := helixnet.NewProdNetworkWithFabric(addr, fabric)
	if err != nil {
		t.Fatalf("NewProdNetworkWithFabric(%q): %v", addr, err)
	}
	return n
}

// newIsolatedProdPair creates two ProdNetworks on the same fresh fabric.
func newIsolatedProdPair(t *testing.T, addrA, addrB string) (*helixnet.ProdNetwork, *helixnet.ProdNetwork) {
	t.Helper()
	fabric := helixnet.NewProdFabric()
	a, err := helixnet.NewProdNetworkWithFabric(addrA, fabric)
	if err != nil {
		t.Fatalf("NewProdNetworkWithFabric(%q): %v", addrA, err)
	}
	b, err := helixnet.NewProdNetworkWithFabric(addrB, fabric)
	if err != nil {
		t.Fatalf("NewProdNetworkWithFabric(%q): %v", addrB, err)
	}
	return a, b
}

// ── ProdNetwork tests ──────────────────────────────────────────────────────

// TestProdNetwork_LocalAddr checks that LocalAddr returns the address supplied
// at construction time.
func TestProdNetwork_LocalAddr(t *testing.T) {
	t.Parallel()
	addr := uniqueAddr(t, "A")
	n := newIsolatedProdNet(t, addr)
	defer n.Close()
	if got := n.LocalAddr(); got != addr {
		t.Errorf("LocalAddr() = %q; want %q", got, addr)
	}
}

// TestProdNetwork_SendReceive is the SINK-SIDE BITE: A.Send(msg) -> B.Receive
// must return the *exact* bytes and from==A.addr.  If Send discards the
// payload this test fails.
func TestProdNetwork_SendReceive(t *testing.T) {
	t.Parallel()
	addrA := uniqueAddr(t, "A")
	addrB := uniqueAddr(t, "B")
	a, b := newIsolatedProdPair(t, addrA, addrB)
	defer a.Close()
	defer b.Close()

	want := []byte("hello helix network")
	if err := a.Send(addrB, want); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got, from, err := b.Receive(2 * time.Second)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	// Sink-side assertion: exact payload.
	if !bytes.Equal(got, want) {
		t.Errorf("payload mismatch: got %q; want %q", got, want)
	}
	// Sink-side assertion: correct sender address.
	if from != addrA {
		t.Errorf("from = %q; want %q", from, addrA)
	}
}

// TestProdNetwork_PayloadIsolation verifies the transport clones the payload,
// so mutating the original after Send does not corrupt what B receives.
func TestProdNetwork_PayloadIsolation(t *testing.T) {
	t.Parallel()
	addrA := uniqueAddr(t, "A")
	addrB := uniqueAddr(t, "B")
	a, b := newIsolatedProdPair(t, addrA, addrB)
	defer a.Close()
	defer b.Close()

	buf := []byte("original")
	if err := a.Send(addrB, buf); err != nil {
		t.Fatalf("Send: %v", err)
	}
	buf[0] = 'X' // mutate after send

	got, _, err := b.Receive(2 * time.Second)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if !bytes.Equal(got, []byte("original")) {
		t.Errorf("payload was not cloned: got %q", got)
	}
}

// TestProdNetwork_ReceiveTimeout: if nobody sends within the timeout, Receive
// must return ErrTimeout — NOT block indefinitely.
func TestProdNetwork_ReceiveTimeout(t *testing.T) {
	t.Parallel()
	n := newIsolatedProdNet(t, uniqueAddr(t, "A"))
	defer n.Close()

	_, _, err := n.Receive(50 * time.Millisecond)
	if err != helixnet.ErrTimeout {
		t.Errorf("expected ErrTimeout; got %v", err)
	}
}

// TestProdNetwork_SendToUnknownAddr: sending to an address that was never
// registered must return ErrUnknownAddr.
func TestProdNetwork_SendToUnknownAddr(t *testing.T) {
	t.Parallel()
	n := newIsolatedProdNet(t, uniqueAddr(t, "A"))
	defer n.Close()

	err := n.Send("no-such-addr", []byte("x"))
	if err != helixnet.ErrUnknownAddr {
		t.Errorf("expected ErrUnknownAddr; got %v", err)
	}
}

// TestProdNetwork_ClosedSend: after Close, Send must return ErrClosed.
func TestProdNetwork_ClosedSend(t *testing.T) {
	t.Parallel()
	addrA := uniqueAddr(t, "A")
	addrB := uniqueAddr(t, "B")
	a, b := newIsolatedProdPair(t, addrA, addrB)
	b.Close()

	// a is still open; send to b (which is now closed/deregistered).
	err := a.Send(addrB, []byte("x"))
	if err == nil {
		t.Error("expected error sending to closed/deregistered endpoint; got nil")
	}
	a.Close()
}

// TestProdNetwork_ClosedReceive: after Close, Receive must return ErrClosed.
func TestProdNetwork_ClosedReceive(t *testing.T) {
	t.Parallel()
	n := newIsolatedProdNet(t, uniqueAddr(t, "A"))
	n.Close()

	_, _, err := n.Receive(50 * time.Millisecond)
	if err != helixnet.ErrClosed {
		t.Errorf("expected ErrClosed; got %v", err)
	}
}

// TestProdNetwork_MultiMessage: multiple sends in order must be received in
// the same order.
func TestProdNetwork_MultiMessage(t *testing.T) {
	t.Parallel()
	addrA := uniqueAddr(t, "A")
	addrB := uniqueAddr(t, "B")
	a, b := newIsolatedProdPair(t, addrA, addrB)
	defer a.Close()
	defer b.Close()

	msgs := [][]byte{
		[]byte("first"),
		[]byte("second"),
		[]byte("third"),
	}
	for _, m := range msgs {
		if err := a.Send(addrB, m); err != nil {
			t.Fatalf("Send %q: %v", m, err)
		}
	}
	for i, want := range msgs {
		got, _, err := b.Receive(2 * time.Second)
		if err != nil {
			t.Fatalf("Receive[%d]: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("msg[%d]: got %q; want %q", i, got, want)
		}
	}
}

// ── SimNetwork tests ───────────────────────────────────────────────────────

// newSimPair creates two SimNetworks sharing one engine and its fabric.
// Defers cleanup of the engine fabric.
func newSimPair(t *testing.T, seed int64, addrA, addrB string) (*dst.Engine, *helixnet.SimNetwork, *helixnet.SimNetwork) {
	t.Helper()
	eng := dst.NewEngine(seed)
	a, err := helixnet.NewSimNetwork(eng, addrA)
	if err != nil {
		t.Fatalf("NewSimNetwork(%q): %v", addrA, err)
	}
	b, err := helixnet.NewSimNetwork(eng, addrB)
	if err != nil {
		t.Fatalf("NewSimNetwork(%q): %v", addrB, err)
	}
	t.Cleanup(func() {
		a.Close()
		b.Close()
		helixnet.RemoveSimFabric(eng)
	})
	return eng, a, b
}

// TestSimNetwork_LocalAddr checks LocalAddr.
func TestSimNetwork_LocalAddr(t *testing.T) {
	t.Parallel()
	eng := dst.NewEngine(42)
	addr := uniqueAddr(t, "A")
	n, err := helixnet.NewSimNetwork(eng, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	defer helixnet.RemoveSimFabric(eng)
	if got := n.LocalAddr(); got != addr {
		t.Errorf("LocalAddr() = %q; want %q", got, addr)
	}
}

// TestSimNetwork_SendReceive_AfterRun: SINK-SIDE BITE for sim.
// Schedule 2 sends, run engine, then Receive must return the exact payloads.
func TestSimNetwork_SendReceive_AfterRun(t *testing.T) {
	t.Parallel()
	addrA := uniqueAddr(t, "A")
	addrB := uniqueAddr(t, "B")
	eng, a, b := newSimPair(t, 1234, addrA, addrB)

	msgA := []byte("sim-message-A->B-1")
	msgB := []byte("sim-message-A->B-2")

	if err := a.Send(addrB, msgA); err != nil {
		t.Fatalf("Send msgA: %v", err)
	}
	if err := a.Send(addrB, msgB); err != nil {
		t.Fatalf("Send msgB: %v", err)
	}

	// Drive the engine to deliver all pending events.
	processed := eng.Run(100)
	if processed == 0 {
		t.Fatal("eng.Run returned 0 — no events were delivered")
	}

	// Receive first message.
	got1, from1, err := b.Receive(0)
	if err != nil {
		t.Fatalf("Receive[0]: %v", err)
	}
	if !bytes.Equal(got1, msgA) {
		t.Errorf("msg[0]: got %q; want %q", got1, msgA)
	}
	if from1 != addrA {
		t.Errorf("from[0] = %q; want %q", from1, addrA)
	}

	// Receive second message.
	got2, from2, err := b.Receive(0)
	if err != nil {
		t.Fatalf("Receive[1]: %v", err)
	}
	if !bytes.Equal(got2, msgB) {
		t.Errorf("msg[1]: got %q; want %q", got2, msgB)
	}
	if from2 != addrA {
		t.Errorf("from[1] = %q; want %q", from2, addrA)
	}
}

// TestSimNetwork_Determinism is the DETERMINISM BITE: same seed twice must
// yield identical delivery order.  Non-deterministic order (or dropped
// messages) makes this test fail.
func TestSimNetwork_Determinism(t *testing.T) {
	t.Parallel()

	const seed = int64(9999)

	runOne := func(addrA, addrB string) [][]byte {
		eng := dst.NewEngine(seed)
		a, err := helixnet.NewSimNetwork(eng, addrA)
		if err != nil {
			t.Fatalf("NewSimNetwork(A): %v", err)
		}
		b, err := helixnet.NewSimNetwork(eng, addrB)
		if err != nil {
			t.Fatalf("NewSimNetwork(B): %v", err)
		}
		defer a.Close()
		defer b.Close()
		defer helixnet.RemoveSimFabric(eng)

		payloads := [][]byte{
			[]byte("msg-0"),
			[]byte("msg-1"),
			[]byte("msg-2"),
		}
		for _, p := range payloads {
			if err := a.Send(addrB, p); err != nil {
				t.Fatalf("Send: %v", err)
			}
		}
		eng.Run(100)

		var out [][]byte
		for {
			msg, _, err := b.Receive(0)
			if err == helixnet.ErrTimeout {
				break
			}
			if err != nil {
				t.Fatalf("Receive: %v", err)
			}
			out = append(out, msg)
		}
		return out
	}

	// Use unique addresses per run to avoid fabric collisions.
	run1 := runOne(uniqueAddr(t, "run1-A"), uniqueAddr(t, "run1-B"))
	run2 := runOne(uniqueAddr(t, "run2-A"), uniqueAddr(t, "run2-B"))

	if len(run1) != len(run2) {
		t.Fatalf("determinism: run1 got %d msgs, run2 got %d msgs", len(run1), len(run2))
	}
	for i := range run1 {
		if !bytes.Equal(run1[i], run2[i]) {
			t.Errorf("determinism: msg[%d] differs: run1=%q run2=%q", i, run1[i], run2[i])
		}
	}
	if len(run1) == 0 {
		t.Error("determinism: no messages received in either run")
	}
}

// TestSimNetwork_NoRunNoDeliver: without calling eng.Run, Receive must time
// out because no events have been processed.
func TestSimNetwork_NoRunNoDeliver(t *testing.T) {
	t.Parallel()
	addrA := uniqueAddr(t, "A")
	addrB := uniqueAddr(t, "B")
	_, a, b := newSimPair(t, 42, addrA, addrB)

	if err := a.Send(addrB, []byte("payload")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Do NOT call eng.Run.
	_, _, err := b.Receive(0)
	if err != helixnet.ErrTimeout {
		t.Errorf("expected ErrTimeout without eng.Run; got %v", err)
	}
}

// TestSimNetwork_ClosedReceive: after Close, Receive returns ErrClosed.
func TestSimNetwork_ClosedReceive(t *testing.T) {
	t.Parallel()
	eng := dst.NewEngine(1)
	addr := uniqueAddr(t, "A")
	n, err := helixnet.NewSimNetwork(eng, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer helixnet.RemoveSimFabric(eng)

	n.Close()
	_, _, err = n.Receive(50 * time.Millisecond)
	if err != helixnet.ErrClosed {
		t.Errorf("expected ErrClosed; got %v", err)
	}
}

// TestSimNetwork_ClosedSend: after Close, Send returns ErrClosed.
func TestSimNetwork_ClosedSend(t *testing.T) {
	t.Parallel()
	eng := dst.NewEngine(1)
	addrA := uniqueAddr(t, "A")
	addrB := uniqueAddr(t, "B")
	a, err := helixnet.NewSimNetwork(eng, addrA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := helixnet.NewSimNetwork(eng, addrB)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	defer helixnet.RemoveSimFabric(eng)

	a.Close()
	err = a.Send(addrB, []byte("x"))
	if err != helixnet.ErrClosed {
		t.Errorf("expected ErrClosed after Close; got %v", err)
	}
}

// TestProdNetwork_BidirectionalExchange: both A->B and B->A exchanges.
func TestProdNetwork_BidirectionalExchange(t *testing.T) {
	t.Parallel()
	addrA := uniqueAddr(t, "A")
	addrB := uniqueAddr(t, "B")
	a, b := newIsolatedProdPair(t, addrA, addrB)
	defer a.Close()
	defer b.Close()

	ping := []byte("ping")
	pong := []byte("pong")

	if err := a.Send(addrB, ping); err != nil {
		t.Fatalf("A.Send(ping): %v", err)
	}
	got, from, err := b.Receive(2 * time.Second)
	if err != nil {
		t.Fatalf("B.Receive: %v", err)
	}
	if !bytes.Equal(got, ping) || from != addrA {
		t.Errorf("B got (%q, %q); want (%q, %q)", got, from, ping, addrA)
	}

	if err := b.Send(addrA, pong); err != nil {
		t.Fatalf("B.Send(pong): %v", err)
	}
	got, from, err = a.Receive(2 * time.Second)
	if err != nil {
		t.Fatalf("A.Receive: %v", err)
	}
	if !bytes.Equal(got, pong) || from != addrB {
		t.Errorf("A got (%q, %q); want (%q, %q)", got, from, pong, addrB)
	}
}
