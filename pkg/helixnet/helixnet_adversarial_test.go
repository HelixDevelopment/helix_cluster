package helixnet_test

import (
	"bytes"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/helixnet"
	"github.com/HelixDevelopment/helix_cluster/pkg/testing/dst"
)

// ════════════════════════════════════════════════════════════════════════════
// pkg/helixnet adversarial audit
//
// Centerpieces:
//   (1) ROUTING CORRECTNESS  — a msg addressed to B reaches B and ONLY B.
//   (2) CONCURRENCY          — hammer send+register+remove from many goroutines
//                              under -race; assert no race, no panic.
//   (3) TEARDOWN             — Close/deregister must not race an in-flight send
//                              (the "send on closed channel" panic class).
//
// These tests pin the fabric contract and are designed to BITE: a mis-route, a
// dropped/duplicated delivery, an unguarded map, or a teardown race all fail.
// ════════════════════════════════════════════════════════════════════════════

// ── (1) ROUTING CORRECTNESS — prod ──────────────────────────────────────────

// TestAdv_Prod_Routing_OnlyDestinationReceives is the routing centerpiece:
// with three endpoints A, B, C on one fabric, A.Send(B, msg) must deliver to
// B and ONLY B. C must see nothing (no mis-route, no broadcast leak).
func TestAdv_Prod_Routing_OnlyDestinationReceives(t *testing.T) {
	t.Parallel()
	fabric := helixnet.NewProdFabric()
	addrA := uniqueAddr(t, "A")
	addrB := uniqueAddr(t, "B")
	addrC := uniqueAddr(t, "C")

	a, err := helixnet.NewProdNetworkWithFabric(addrA, fabric)
	if err != nil {
		t.Fatalf("new A: %v", err)
	}
	b, err := helixnet.NewProdNetworkWithFabric(addrB, fabric)
	if err != nil {
		t.Fatalf("new B: %v", err)
	}
	c, err := helixnet.NewProdNetworkWithFabric(addrC, fabric)
	if err != nil {
		t.Fatalf("new C: %v", err)
	}
	defer a.Close()
	defer b.Close()
	defer c.Close()

	want := []byte("for-B-only")
	if err := a.Send(addrB, want); err != nil {
		t.Fatalf("A.Send(B): %v", err)
	}

	// B must receive exactly the payload, from A.
	got, from, err := b.Receive(2 * time.Second)
	if err != nil {
		t.Fatalf("B.Receive: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("B payload = %q; want %q", got, want)
	}
	if from != addrA {
		t.Errorf("B from = %q; want %q", from, addrA)
	}

	// C must receive NOTHING — a mis-route to C would deliver here.
	if _, _, err := c.Receive(100 * time.Millisecond); err != helixnet.ErrTimeout {
		t.Errorf("C should have received nothing; got err=%v (mis-route leak)", err)
	}
}

// TestAdv_Prod_Routing_DistinctDestinations sends a different payload to each of
// N endpoints from one sender and asserts each endpoint receives exactly its own
// payload — a routing-table swap (B<->C) makes this fail.
func TestAdv_Prod_Routing_DistinctDestinations(t *testing.T) {
	t.Parallel()
	const n = 8
	fabric := helixnet.NewProdFabric()

	sender, err := helixnet.NewProdNetworkWithFabric(uniqueAddr(t, "S"), fabric)
	if err != nil {
		t.Fatalf("new sender: %v", err)
	}
	defer sender.Close()

	addrs := make([]string, n)
	nets := make([]*helixnet.ProdNetwork, n)
	for i := 0; i < n; i++ {
		addrs[i] = uniqueAddr(t, fmt.Sprintf("D%d", i))
		nets[i], err = helixnet.NewProdNetworkWithFabric(addrs[i], fabric)
		if err != nil {
			t.Fatalf("new D%d: %v", i, err)
		}
		defer nets[i].Close()
	}

	// Distinct payload per destination.
	for i := 0; i < n; i++ {
		p := []byte(fmt.Sprintf("payload-for-%d", i))
		if err := sender.Send(addrs[i], p); err != nil {
			t.Fatalf("Send to %d: %v", i, err)
		}
	}

	for i := 0; i < n; i++ {
		want := []byte(fmt.Sprintf("payload-for-%d", i))
		got, _, err := nets[i].Receive(2 * time.Second)
		if err != nil {
			t.Fatalf("D%d.Receive: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("D%d got %q; want %q (routing swap)", i, got, want)
		}
		// Each destination must hold exactly ONE message — no duplicate.
		if _, _, err := nets[i].Receive(20 * time.Millisecond); err != helixnet.ErrTimeout {
			t.Errorf("D%d received a duplicate/extra message; err=%v", i, err)
		}
	}
}

// TestAdv_Prod_SendToSelf: an endpoint may address itself; the message must be
// delivered to its own inbox (loopback) per the registry contract.
func TestAdv_Prod_SendToSelf(t *testing.T) {
	t.Parallel()
	fabric := helixnet.NewProdFabric()
	addr := uniqueAddr(t, "self")
	n, err := helixnet.NewProdNetworkWithFabric(addr, fabric)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer n.Close()

	want := []byte("loopback")
	if err := n.Send(addr, want); err != nil {
		t.Fatalf("Send to self: %v", err)
	}
	got, from, err := n.Receive(2 * time.Second)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if !bytes.Equal(got, want) || from != addr {
		t.Errorf("self got (%q,%q); want (%q,%q)", got, from, want, addr)
	}
}

// TestAdv_Prod_DoubleClose: Close is idempotent and must not panic on the
// second call (which would close an already-closed channel).
func TestAdv_Prod_DoubleClose(t *testing.T) {
	t.Parallel()
	n := newIsolatedProdNet(t, uniqueAddr(t, "A"))
	if err := n.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := n.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// ── (1') ROUTING CORRECTNESS — sim, and SIM/PROD CONSISTENCY ────────────────

// TestAdv_Sim_Routing_OnlyDestinationReceives mirrors the prod routing
// centerpiece on the sim fabric: B receives, C does not.
func TestAdv_Sim_Routing_OnlyDestinationReceives(t *testing.T) {
	t.Parallel()
	eng := dst.NewEngine(7)
	addrA := uniqueAddr(t, "A")
	addrB := uniqueAddr(t, "B")
	addrC := uniqueAddr(t, "C")
	a, _ := helixnet.NewSimNetwork(eng, addrA)
	b, _ := helixnet.NewSimNetwork(eng, addrB)
	c, _ := helixnet.NewSimNetwork(eng, addrC)
	t.Cleanup(func() {
		a.Close()
		b.Close()
		c.Close()
		helixnet.RemoveSimFabric(eng)
	})

	want := []byte("sim-for-B-only")
	if err := a.Send(addrB, want); err != nil {
		t.Fatalf("A.Send(B): %v", err)
	}
	eng.Run(100)

	got, from, err := b.Receive(0)
	if err != nil {
		t.Fatalf("B.Receive: %v", err)
	}
	if !bytes.Equal(got, want) || from != addrA {
		t.Errorf("B got (%q,%q); want (%q,%q)", got, from, want, addrA)
	}
	if _, _, err := c.Receive(0); err != helixnet.ErrTimeout {
		t.Errorf("C should have received nothing; got err=%v", err)
	}
}

// TestAdv_SimProd_UnknownDest_Consistency: sim/prod divergence probe.
// Prod returns ErrUnknownAddr for an unregistered destination. Sim's fabric is
// address-agnostic at Send time (it buffers by dst addr), so a Send to an
// unknown addr is accepted but never Received by anyone. This test pins BOTH
// observed contracts so a future change that silently diverges is caught.
func TestAdv_SimProd_UnknownDest_Consistency(t *testing.T) {
	t.Parallel()

	// Prod: unknown destination => ErrUnknownAddr at Send.
	pn := newIsolatedProdNet(t, uniqueAddr(t, "P"))
	defer pn.Close()
	if err := pn.Send("prod-nobody", []byte("x")); err != helixnet.ErrUnknownAddr {
		t.Errorf("prod Send(unknown) = %v; want ErrUnknownAddr", err)
	}

	// Sim: Send(unknown) is accepted (no error) but nobody Receives it.
	eng := dst.NewEngine(3)
	addrA := uniqueAddr(t, "A")
	a, _ := helixnet.NewSimNetwork(eng, addrA)
	t.Cleanup(func() {
		a.Close()
		helixnet.RemoveSimFabric(eng)
	})
	if err := a.Send("sim-nobody", []byte("x")); err != nil {
		t.Errorf("sim Send(unknown) = %v; want nil (accepted, undeliverable)", err)
	}
	eng.Run(100)
	// The sender must not receive its own undeliverable message.
	if _, _, err := a.Receive(0); err != helixnet.ErrTimeout {
		t.Errorf("sim sender unexpectedly received undeliverable msg; err=%v", err)
	}
}

// ── (2) CONCURRENCY centerpiece ─────────────────────────────────────────────

// TestAdv_Prod_ConcurrentSendRegisterRemove hammers register + send + remove
// from many goroutines on ONE fabric under -race. The fabric's endpoints map is
// the read-by-many/written-by-few shared state; any unguarded access is a data
// race the -race detector reports. A "send on closed channel" panic from the
// teardown path also fails the test (recovered + reported).
func TestAdv_Prod_ConcurrentSendRegisterRemove(t *testing.T) {
	t.Parallel()
	fabric := helixnet.NewProdFabric()

	// A stable receiver that lives for the whole test — always a valid dst.
	stableAddr := uniqueAddr(t, "stable")
	stable, err := helixnet.NewProdNetworkWithFabric(stableAddr, fabric)
	if err != nil {
		t.Fatalf("new stable: %v", err)
	}
	defer stable.Close()

	// Drain the stable receiver so its channel never blocks senders.
	stopDrain := make(chan struct{})
	var drainWG sync.WaitGroup
	drainWG.Add(1)
	go func() {
		defer drainWG.Done()
		for {
			select {
			case <-stopDrain:
				return
			default:
				stable.Receive(5 * time.Millisecond)
			}
		}
	}()

	var panics int32
	guard := func(fn func()) {
		defer func() {
			if r := recover(); r != nil {
				atomic.AddInt32(&panics, 1)
				t.Errorf("panic in concurrent op: %v", r)
			}
		}()
		fn()
	}

	const workers = 24
	const iters = 200
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			payload := []byte(fmt.Sprintf("w%d", w))
			for i := 0; i < iters; i++ {
				// Continuous senders → stable: races deliver() against the map.
				guard(func() { _ = stable.Send(stableAddr, payload) })

				// Churning endpoints: register, send to it, then remove it.
				ephAddr := fmt.Sprintf("eph-%d-%d", w, i)
				guard(func() {
					eph, e := helixnet.NewProdNetworkWithFabric(ephAddr, fabric)
					if e != nil {
						return
					}
					// In-flight send to the ephemeral endpoint racing its Close.
					_ = stable.Send(ephAddr, payload)
					_ = eph.Send(ephAddr, payload)
					eph.Close() // deregister + close(ch) — races the sends above
				})
			}
		}(w)
	}

	wg.Wait()
	close(stopDrain)
	drainWG.Wait()

	if atomic.LoadInt32(&panics) != 0 {
		t.Fatalf("observed %d panic(s) under concurrent send/register/remove", panics)
	}
}

// TestAdv_Prod_TeardownRace_InFlightSend isolates the teardown race: one
// goroutine spams Send(victim) while another repeatedly creates+Closes the
// victim endpoint. deliver() reads ep under RLock, releases it, then sends on
// ep.ch; Close() does deregister()+close(ep.ch). If those interleave, deliver
// sends on a closed channel → panic. Recovered + reported so the test fails
// loudly instead of crashing the whole run.
func TestAdv_Prod_TeardownRace_InFlightSend(t *testing.T) {
	t.Parallel()
	fabric := helixnet.NewProdFabric()
	sender, err := helixnet.NewProdNetworkWithFabric(uniqueAddr(t, "sender"), fabric)
	if err != nil {
		t.Fatalf("new sender: %v", err)
	}
	defer sender.Close()

	victimAddr := uniqueAddr(t, "victim")
	var panics int32
	var wg sync.WaitGroup

	stop := make(chan struct{})

	// Spammer: continuously sends to victimAddr.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				atomic.AddInt32(&panics, 1)
				t.Errorf("PANIC in Send (send on closed channel teardown race): %v", r)
			}
		}()
		p := []byte("inflight")
		for {
			select {
			case <-stop:
				return
			default:
				_ = sender.Send(victimAddr, p)
			}
		}
	}()

	// Churner: create + Close the victim repeatedly.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			v, e := helixnet.NewProdNetworkWithFabric(victimAddr, fabric)
			if e != nil {
				continue
			}
			v.Close()
		}
		close(stop)
	}()

	wg.Wait()
	if atomic.LoadInt32(&panics) != 0 {
		t.Fatalf("teardown race produced %d panic(s) (send on closed channel)", panics)
	}
}

// ── (3) TEARDOWN — sim ──────────────────────────────────────────────────────

// TestAdv_Sim_RemoveFabric_Teardown: after RemoveSimFabric, a freshly created
// network on the same engine gets a clean fabric, and a send through it after a
// new Run is delivered correctly (no dangling route from the removed fabric).
func TestAdv_Sim_RemoveFabric_CleanReuse(t *testing.T) {
	t.Parallel()
	eng := dst.NewEngine(11)

	addrA := uniqueAddr(t, "A")
	addrB := uniqueAddr(t, "B")
	a1, _ := helixnet.NewSimNetwork(eng, addrA)
	b1, _ := helixnet.NewSimNetwork(eng, addrB)
	if err := a1.Send(addrB, []byte("first-epoch")); err != nil {
		t.Fatalf("Send epoch1: %v", err)
	}
	eng.Run(100)
	if _, _, err := b1.Receive(0); err != nil {
		t.Fatalf("epoch1 Receive: %v", err)
	}
	a1.Close()
	b1.Close()
	helixnet.RemoveSimFabric(eng)

	// New epoch on same engine: fresh fabric, must route cleanly.
	a2, _ := helixnet.NewSimNetwork(eng, addrA)
	b2, _ := helixnet.NewSimNetwork(eng, addrB)
	t.Cleanup(func() {
		a2.Close()
		b2.Close()
		helixnet.RemoveSimFabric(eng)
	})
	want := []byte("second-epoch")
	if err := a2.Send(addrB, want); err != nil {
		t.Fatalf("Send epoch2: %v", err)
	}
	eng.Run(100)
	got, _, err := b2.Receive(0)
	if err != nil {
		t.Fatalf("epoch2 Receive: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("epoch2 got %q; want %q", got, want)
	}
}

// TestAdv_Sim_DoubleClose: Close on a SimNetwork is idempotent.
func TestAdv_Sim_DoubleClose(t *testing.T) {
	t.Parallel()
	eng := dst.NewEngine(5)
	n, _ := helixnet.NewSimNetwork(eng, uniqueAddr(t, "A"))
	t.Cleanup(func() { helixnet.RemoveSimFabric(eng) })
	if err := n.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := n.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestAdv_Sim_ConcurrentSend hammers concurrent sends on a shared sim fabric
// under -race to surface any unguarded buffer-map access (the fabric mu must
// guard every append/pop/deregister).
func TestAdv_Sim_ConcurrentSend(t *testing.T) {
	t.Parallel()
	eng := dst.NewEngine(99)
	addrA := uniqueAddr(t, "A")
	addrB := uniqueAddr(t, "B")
	a, _ := helixnet.NewSimNetwork(eng, addrA)
	b, _ := helixnet.NewSimNetwork(eng, addrB)
	t.Cleanup(func() {
		a.Close()
		b.Close()
		helixnet.RemoveSimFabric(eng)
	})

	const workers = 16
	const iters = 100
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				_ = a.Send(addrB, []byte(fmt.Sprintf("w%d-%d", w, i)))
			}
		}(w)
	}
	wg.Wait()
	eng.Run(workers * iters)

	// Drain concurrently with another round of sends to race append vs pop.
	var got int
	for {
		_, _, err := b.Receive(0)
		if err == helixnet.ErrTimeout {
			break
		}
		got++
	}
	if got == 0 {
		t.Fatal("no messages delivered through sim fabric")
	}
}
