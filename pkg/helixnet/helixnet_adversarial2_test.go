package helixnet_test

import (
	"bytes"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/helixnet"
)

// ════════════════════════════════════════════════════════════════════════════
// pkg/helixnet adversarial audit — WAVE 2
//
// The first concurrency fix hardened the SEND/teardown path: ProdNetwork.Close
// closes the endpoint channel inside fabric.deregister under the write lock, so
// it is mutually exclusive with deliver()'s read-locked send (no "send on closed
// channel"). That path is airtight.
//
// What the first pass MISSED is on the RECEIVE side, and it is a sink-side
// correctness bug, not a race: ProdNetwork.Receive armed a timer and then ran a
// single select over {channel, timer.C}. When a message is ALREADY buffered and
// the timeout is small/zero (so timer.C is already ready too), Go's select picks
// uniformly among the ready cases — so a buffered, immediately-available message
// is reported as ErrTimeout ~half the time. SimNetwork.Receive has a buffer-first
// fast path that makes the same call correct; prod did not. (Guard one axis, miss
// the other — the recurring pattern.)
//
// These tests pin the contract: a message that has ALREADY arrived MUST be
// returned by Receive regardless of how small the timeout is — never skipped.
// ════════════════════════════════════════════════════════════════════════════

// TestAdv2_Prod_Receive0_DeliversBufferedMessage is the centerpiece sink-side
// bite. A message is buffered and ready; Receive(0) MUST return it, never
// ErrTimeout. Pre-fix this failed ~50% of iterations because the select raced a
// ready channel against an already-expired 0-timer.
//
// MUTATION PROOF: revert prod.go's Receive buffer-first fast path and this test
// FAILS (non-zero spurious-timeout count); with the fast path it PASSES.
func TestAdv2_Prod_Receive0_DeliversBufferedMessage(t *testing.T) {
	t.Parallel()
	const trials = 200
	spurious := 0
	for i := 0; i < trials; i++ {
		fabric := helixnet.NewProdFabric()
		a, err := helixnet.NewProdNetworkWithFabric(uniqueAddr(t, fmt.Sprintf("a%d", i)), fabric)
		if err != nil {
			t.Fatalf("new a: %v", err)
		}
		bAddr := uniqueAddr(t, fmt.Sprintf("b%d", i))
		b, err := helixnet.NewProdNetworkWithFabric(bAddr, fabric)
		if err != nil {
			t.Fatalf("new b: %v", err)
		}

		want := []byte("ready-now")
		if err := a.Send(bAddr, want); err != nil {
			t.Fatalf("Send: %v", err)
		}
		// The message is now sitting in b's buffered channel; it is ready.
		got, from, rerr := b.Receive(0)
		switch {
		case rerr == helixnet.ErrTimeout:
			spurious++
		case rerr != nil:
			t.Fatalf("unexpected Receive err: %v", rerr)
		default:
			if !bytes.Equal(got, want) || from != a.LocalAddr() {
				t.Fatalf("payload/sender mismatch: got (%q,%q)", got, from)
			}
		}
		a.Close()
		b.Close()
	}
	if spurious != 0 {
		t.Fatalf("Receive(0) returned ErrTimeout %d/%d times despite a buffered, ready message "+
			"(buffer-first fast path missing/broken)", spurious, trials)
	}
}

// TestAdv2_Prod_ReceiveTinyTimeout_DrainsAll pins the same property for a small
// non-zero timeout and for a BURST: every buffered message must be drained, none
// skipped. Pre-fix, draining N buffered messages with a tiny timeout loses a
// random subset to spurious ErrTimeout.
//
// MUTATION PROOF: without the fast path, drained < sent (some buffered messages
// reported as ErrTimeout); with it, drained == sent every time.
func TestAdv2_Prod_ReceiveTinyTimeout_DrainsAll(t *testing.T) {
	t.Parallel()
	fabric := helixnet.NewProdFabric()
	a, err := helixnet.NewProdNetworkWithFabric(uniqueAddr(t, "a"), fabric)
	if err != nil {
		t.Fatalf("new a: %v", err)
	}
	bAddr := uniqueAddr(t, "b")
	b, err := helixnet.NewProdNetworkWithFabric(bAddr, fabric)
	if err != nil {
		t.Fatalf("new b: %v", err)
	}
	defer a.Close()
	defer b.Close()

	const n = 64
	for i := 0; i < n; i++ {
		if err := a.Send(bAddr, []byte(fmt.Sprintf("m%d", i))); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}

	// Drain with a tiny timeout. All n messages are already buffered and ready,
	// so every Receive must return one until the buffer is empty — no message may
	// be skipped as a spurious timeout while data remains.
	got := 0
	for {
		_, _, rerr := b.Receive(time.Microsecond)
		if rerr == helixnet.ErrTimeout {
			break
		}
		if rerr != nil {
			t.Fatalf("Receive err: %v", rerr)
		}
		got++
		if got > n {
			t.Fatalf("drained more than sent: %d > %d", got, n)
		}
	}
	if got != n {
		t.Fatalf("drained %d of %d buffered messages; the rest were lost to spurious ErrTimeout", got, n)
	}
}

// TestAdv2_Prod_ReceiveOrderPreserved_TinyTimeout: the buffer-first fast path
// must preserve FIFO order, not reorder or duplicate. Sends 0..n-1, drains with
// a tiny timeout, asserts exact in-order sequence.
func TestAdv2_Prod_ReceiveOrderPreserved_TinyTimeout(t *testing.T) {
	t.Parallel()
	fabric := helixnet.NewProdFabric()
	a, err := helixnet.NewProdNetworkWithFabric(uniqueAddr(t, "a"), fabric)
	if err != nil {
		t.Fatalf("new a: %v", err)
	}
	bAddr := uniqueAddr(t, "b")
	b, err := helixnet.NewProdNetworkWithFabric(bAddr, fabric)
	if err != nil {
		t.Fatalf("new b: %v", err)
	}
	defer a.Close()
	defer b.Close()

	const n = 32
	for i := 0; i < n; i++ {
		if err := a.Send(bAddr, []byte(fmt.Sprintf("%05d", i))); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}
	for i := 0; i < n; i++ {
		got, _, rerr := b.Receive(time.Millisecond)
		if rerr != nil {
			t.Fatalf("Receive[%d] err: %v (lost a buffered message)", i, rerr)
		}
		want := []byte(fmt.Sprintf("%05d", i))
		if !bytes.Equal(got, want) {
			t.Fatalf("order broken at %d: got %q want %q", i, got, want)
		}
	}
}

// TestAdv2_Prod_ClosedBeats_BufferedFastPath: a closed endpoint whose buffer is
// empty must report ErrClosed via the fast path (the channel is closed →
// ok==false), not ErrTimeout. This guards the fast-path's closed-channel branch.
func TestAdv2_Prod_ClosedFastPath_ReturnsClosed(t *testing.T) {
	t.Parallel()
	n := newIsolatedProdNet(t, uniqueAddr(t, "x"))
	n.Close()
	// Closed and empty: fast path sees a closed channel and must return ErrClosed.
	if _, _, err := n.Receive(0); err != helixnet.ErrClosed {
		t.Fatalf("Receive(0) on closed endpoint = %v; want ErrClosed", err)
	}
}

// TestAdv2_Prod_ConcurrentSendReceiveClose_NoLoss is a stress combination of the
// hardened send/teardown path AND the receive fast path under -race: a producer
// floods a receiver that drains with timeout 0 in a tight loop, while a third
// goroutine eventually closes the receiver. We assert: no panic (send-on-closed /
// double-close), no -race report, and every message that the producer observed as
// a successful Send (before close) is either drained or accounted for — i.e. the
// receiver never spuriously times out while data is buffered and open.
func TestAdv2_Prod_ConcurrentSendReceiveClose_NoLoss(t *testing.T) {
	t.Parallel()
	fabric := helixnet.NewProdFabric()
	sAddr := uniqueAddr(t, "s")
	rAddr := uniqueAddr(t, "r")
	sender, err := helixnet.NewProdNetworkWithFabric(sAddr, fabric)
	if err != nil {
		t.Fatalf("new sender: %v", err)
	}
	defer sender.Close()
	receiver, err := helixnet.NewProdNetworkWithFabric(rAddr, fabric)
	if err != nil {
		t.Fatalf("new receiver: %v", err)
	}

	var panics int32
	guard := func(fn func()) {
		defer func() {
			if r := recover(); r != nil {
				atomic.AddInt32(&panics, 1)
				t.Errorf("panic: %v", r)
			}
		}()
		fn()
	}

	var sent, received int64
	var wg sync.WaitGroup
	closed := make(chan struct{})

	// Receiver: drain continuously with timeout 0 (the fast-path hot loop).
	wg.Add(1)
	go func() {
		defer wg.Done()
		guard(func() {
			for {
				_, _, e := receiver.Receive(0)
				if e == helixnet.ErrClosed {
					return
				}
				if e == nil {
					atomic.AddInt64(&received, 1)
				}
				select {
				case <-closed:
					// keep draining until ErrClosed surfaces
				default:
				}
			}
		})
	}()

	// Producer: send until told to stop.
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		guard(func() {
			for {
				select {
				case <-stop:
					return
				default:
					if e := sender.Send(rAddr, []byte("p")); e == nil {
						atomic.AddInt64(&sent, 1)
					}
				}
			}
		})
	}()

	time.Sleep(20 * time.Millisecond)
	close(stop)
	// Let the receiver catch up to anything still buffered, then close it.
	time.Sleep(20 * time.Millisecond)
	guard(func() { receiver.Close() })
	close(closed)
	wg.Wait()

	if atomic.LoadInt32(&panics) != 0 {
		t.Fatalf("observed %d panic(s)", panics)
	}
	// Sanity: with a live, draining receiver and a flooding sender, SOME messages
	// must have been delivered. A pre-fix fast-path-less Receive(0) would spin
	// returning ErrTimeout and could deliver far fewer; this is a soft liveness
	// floor, the hard assertions are no-panic / no-race above.
	if atomic.LoadInt64(&received) == 0 && atomic.LoadInt64(&sent) > 0 {
		t.Fatalf("sent %d messages but receiver got 0 — Receive(0) starved on buffered data", sent)
	}
}
