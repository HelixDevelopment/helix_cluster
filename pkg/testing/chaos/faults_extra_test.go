package chaos

import (
	"bytes"
	"errors"
	"math/rand"
	"testing"
	"time"
)

// roundTrip exercises the standard double-apply / double-restore contract shared
// by every fault and asserts the effect predicate transitions correctly.
func roundTrip(t *testing.T, f Fault) {
	t.Helper()
	if err := f.Apply(); err != nil {
		t.Fatalf("%s: first apply: %v", f.Name(), err)
	}
	if err := f.Apply(); err == nil {
		t.Fatalf("%s: expected error on double apply", f.Name())
	}
	if err := f.Restore(); err != nil {
		t.Fatalf("%s: first restore: %v", f.Name(), err)
	}
	if err := f.Restore(); err == nil {
		t.Fatalf("%s: expected error on double restore", f.Name())
	}
}

// --- Network ---------------------------------------------------------------

func TestPacketReorder(t *testing.T) {
	f := &PacketReorder{From: "a", To: "b", Window: 3}
	// Baseline: pass-through in order.
	if got := f.Deliver(1); len(got) != 1 || got[0] != 1 {
		t.Fatalf("baseline expected [1], got %v", got)
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	// First two are buffered.
	if got := f.Deliver(10); got != nil {
		t.Fatalf("expected buffered nil, got %v", got)
	}
	if got := f.Deliver(11); got != nil {
		t.Fatalf("expected buffered nil, got %v", got)
	}
	// Third releases the window reversed: 12,11,10.
	got := f.Deliver(12)
	want := []int{12, 11, 10}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("expected reordered %v, got %v", want, got)
		}
	}
	// Mutation: deleting the reverse loop in Deliver (returning f.buf in order)
	// yields [10,11,12] and fails this assertion.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if got := f.Deliver(7); len(got) != 1 || got[0] != 7 {
		t.Fatalf("post-restore expected [7], got %v", got)
	}
}

func TestPacketDuplication(t *testing.T) {
	f := &PacketDuplication{From: "a", To: "b", Copies: 3}
	if f.DeliverCount() != 1 {
		t.Fatalf("baseline expected 1 copy, got %d", f.DeliverCount())
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	if f.DeliverCount() != 3 {
		t.Fatalf("expected 3 copies, got %d", f.DeliverCount())
	}
	// Mutation: making Apply a no-op leaves DeliverCount==1, failing here.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if f.DeliverCount() != 1 {
		t.Fatalf("post-restore expected 1, got %d", f.DeliverCount())
	}
}

func TestBandwidthThrottle(t *testing.T) {
	f := &BandwidthThrottle{From: "a", To: "b", BytesPerSec: 1000}
	if f.TransmitTime(1000) != 0 {
		t.Fatalf("baseline expected 0, got %v", f.TransmitTime(1000))
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	// 1000 bytes at 1000 B/s == 1s.
	if got := f.TransmitTime(1000); got != time.Second {
		t.Fatalf("expected 1s, got %v", got)
	}
	// Mutation: returning 0 unconditionally from TransmitTime fails this.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if f.TransmitTime(1000) != 0 {
		t.Fatalf("post-restore expected 0, got %v", f.TransmitTime(1000))
	}
}

func TestAsymmetricPartition(t *testing.T) {
	f := &AsymmetricPartition{From: "a", To: "b"}
	if f.Blocked("a", "b") {
		t.Fatal("baseline must not block")
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	if !f.Blocked("a", "b") {
		t.Fatal("expected a->b blocked")
	}
	if f.Blocked("b", "a") {
		t.Fatal("reverse path b->a must stay open")
	}
	// Mutation: Blocked returning false always fails the a->b assertion;
	// returning true always fails the b->a assertion.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if f.Blocked("a", "b") {
		t.Fatal("post-restore must not block")
	}
}

func TestDNSBlackhole(t *testing.T) {
	f := &DNSBlackhole{Hosts: map[string]bool{"db": true}}
	if _, ok := f.Resolve("db"); !ok {
		t.Fatal("baseline must resolve")
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Resolve("db"); ok {
		t.Fatal("expected db blackholed")
	}
	if _, ok := f.Resolve("api"); !ok {
		t.Fatal("non-listed host must still resolve")
	}
	// Mutation: making Resolve ignore applied state keeps db resolvable, fails here.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Resolve("db"); !ok {
		t.Fatal("post-restore db must resolve")
	}
}

// --- Process ---------------------------------------------------------------

func TestProcessPause(t *testing.T) {
	f := &ProcessPause{NodeID: "n1"}
	if f.Tick() != 1 || f.Tick() != 2 {
		t.Fatal("baseline ticks must advance")
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	before := f.Ticks()
	f.Tick()
	f.Tick()
	if f.Ticks() != before {
		t.Fatalf("paused process must not advance: %d != %d", f.Ticks(), before)
	}
	// Mutation: Tick incrementing while applied advances the counter, fails here.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if f.Tick() != before+1 {
		t.Fatal("post-restore tick must resume")
	}
}

func TestOOMKill(t *testing.T) {
	f := &OOMKill{NodeID: "n1", LimitB: 100}
	if !f.Alloc(1_000_000) {
		t.Fatal("baseline alloc must succeed")
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	if !f.Alloc(60) {
		t.Fatal("alloc within limit must succeed")
	}
	if f.Alloc(60) {
		t.Fatal("alloc exceeding limit must be denied")
	}
	// Mutation: Alloc returning true unconditionally fails the denial assertion.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if !f.Alloc(1_000_000) {
		t.Fatal("post-restore alloc must succeed")
	}
}

func TestSlowDisk(t *testing.T) {
	f := &SlowDisk{NodeID: "n1", Factor: 10}
	base := 5 * time.Millisecond
	if f.IOTime(base) != base {
		t.Fatal("baseline IO must be unchanged")
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	if got := f.IOTime(base); got != 50*time.Millisecond {
		t.Fatalf("expected 50ms, got %v", got)
	}
	// Mutation: IOTime returning base unconditionally fails the 50ms assertion.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if f.IOTime(base) != base {
		t.Fatal("post-restore IO must be unchanged")
	}
}

// --- Clock -----------------------------------------------------------------

func TestClockSkew(t *testing.T) {
	now := time.Unix(1000, 0)
	f := &ClockSkew{NodeID: "n1", Offset: 30 * time.Second}
	if !f.Now(now).Equal(now) {
		t.Fatal("baseline clock unchanged")
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	if got := f.Now(now); !got.Equal(now.Add(30 * time.Second)) {
		t.Fatalf("expected +30s, got %v", got)
	}
	// Mutation: Now returning base unconditionally fails the +30s assertion.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if !f.Now(now).Equal(now) {
		t.Fatal("post-restore clock unchanged")
	}
}

func TestClockFreeze(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	f := &ClockFreeze{NodeID: "n1"}
	if !f.Now(t0).Equal(t0) {
		t.Fatal("baseline passes time through")
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	first := f.Now(t0)
	if !f.Now(t1).Equal(first) {
		t.Fatalf("frozen clock must stay at %v, got %v", first, f.Now(t1))
	}
	// Mutation: Now returning base while applied advances to t1, failing here.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if !f.Now(t1).Equal(t1) {
		t.Fatal("post-restore passes time through")
	}
}

func TestClockJump(t *testing.T) {
	now := time.Unix(0, 0)
	f := &ClockJump{NodeID: "n1", Delta: time.Hour}
	if !f.Now(now).Equal(now) {
		t.Fatal("baseline unchanged")
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	if got := f.Now(now); !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("expected +1h, got %v", got)
	}
	// Mutation: Now ignoring Delta fails the +1h assertion.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if !f.Now(now).Equal(now) {
		t.Fatal("post-restore unchanged")
	}
}

// --- Runtime ---------------------------------------------------------------

func TestCPUHog(t *testing.T) {
	f := &CPUHog{NodeID: "n1", Percent: 75}
	base := 100 * time.Millisecond
	if f.WorkTime(base) != base {
		t.Fatal("baseline unchanged")
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	// 25% available -> 4x time == 400ms.
	if got := f.WorkTime(base); got != 400*time.Millisecond {
		t.Fatalf("expected 400ms, got %v", got)
	}
	// Mutation: WorkTime returning base unconditionally fails the 4x assertion.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if f.WorkTime(base) != base {
		t.Fatal("post-restore unchanged")
	}
}

func TestMemPressure(t *testing.T) {
	f := &MemPressure{NodeID: "n1", TotalB: 1000, Percent: 90}
	if f.Available() != 1000 {
		t.Fatal("baseline full headroom")
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	if got := f.Available(); got != 100 {
		t.Fatalf("expected 100 headroom, got %d", got)
	}
	// Mutation: Available returning TotalB unconditionally fails here.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if f.Available() != 1000 {
		t.Fatal("post-restore full headroom")
	}
}

func TestFDExhaust(t *testing.T) {
	f := &FDExhaust{NodeID: "n1", Limit: 2}
	for i := 0; i < 100; i++ {
		if !f.OpenFD() {
			t.Fatal("baseline open must always succeed")
		}
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	if !f.OpenFD() || !f.OpenFD() {
		t.Fatal("first two opens within limit must succeed")
	}
	if f.OpenFD() {
		t.Fatal("third open beyond limit must fail")
	}
	// Mutation: OpenFD returning true unconditionally fails the third-open assertion.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if !f.OpenFD() {
		t.Fatal("post-restore open must succeed")
	}
}

// --- Storage ---------------------------------------------------------------

func TestDiskCorruption(t *testing.T) {
	data := []byte("hello-world-payload")
	f := NewDiskCorruption("n1", rand.New(rand.NewSource(42)))
	if !bytes.Equal(f.Read(data), data) {
		t.Fatal("baseline read must be verbatim")
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	got := f.Read(data)
	if bytes.Equal(got, data) {
		t.Fatal("applied read must differ from input")
	}
	if len(got) != len(data) {
		t.Fatal("corruption must preserve length")
	}
	// Exactly one byte differs.
	diff := 0
	for i := range got {
		if got[i] != data[i] {
			diff++
		}
	}
	if diff != 1 {
		t.Fatalf("expected exactly 1 corrupted byte, got %d", diff)
	}
	// Mutation: Read copying data without flipping a byte makes got==data, fails here.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.Read(data), data) {
		t.Fatal("post-restore read verbatim")
	}
}

func TestDiskCorruptionDeterministic(t *testing.T) {
	data := []byte("deterministic-corruption-check")
	a := NewDiskCorruption("n1", rand.New(rand.NewSource(7)))
	b := NewDiskCorruption("n1", rand.New(rand.NewSource(7)))
	_ = a.Apply()
	_ = b.Apply()
	if !bytes.Equal(a.Read(data), b.Read(data)) {
		t.Fatal("same seed must corrupt identical position")
	}
	// Mutation: seeding from time.Now() instead of the passed rng diverges, fails here.
}

func TestDiskFull(t *testing.T) {
	f := &DiskFull{NodeID: "n1", CapacityB: 100}
	if !f.Write(1_000_000) {
		t.Fatal("baseline write must succeed")
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	if !f.Write(60) {
		t.Fatal("write within capacity must succeed")
	}
	if f.Write(60) {
		t.Fatal("write exceeding capacity must fail")
	}
	// Mutation: Write returning true unconditionally fails the over-capacity assertion.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if !f.Write(1_000_000) {
		t.Fatal("post-restore write must succeed")
	}
}

func TestReadOnlyMount(t *testing.T) {
	f := &ReadOnlyMount{NodeID: "n1"}
	if !f.CanWrite() {
		t.Fatal("baseline writable")
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	if f.CanWrite() {
		t.Fatal("applied mount must be read-only")
	}
	// Mutation: CanWrite returning true unconditionally fails here.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if !f.CanWrite() {
		t.Fatal("post-restore writable")
	}
}

func TestFsyncLoss(t *testing.T) {
	f := &FsyncLoss{NodeID: "n1"}
	f.Write(100)
	if f.Sync() != 100 {
		t.Fatal("baseline sync must persist dirty data")
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	before := f.Durable()
	f.Write(500)
	if f.Sync() != before {
		t.Fatalf("fsync-loss must discard unflushed data, durable changed from %d", before)
	}
	if f.Durable() != before {
		t.Fatal("durable bytes must not grow during loss window")
	}
	// Mutation: Sync persisting dirty while applied grows durable, fails here.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	f.Write(200)
	if f.Sync() != before+200 {
		t.Fatal("post-restore sync must persist again")
	}
}

// --- RPC -------------------------------------------------------------------

func TestRPCError(t *testing.T) {
	f := &RPCError{Method: "Get"}
	if f.Call("Get") != nil {
		t.Fatal("baseline must not inject error")
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	if err := f.Call("Get"); !errors.Is(err, ErrInjectedRPC) {
		t.Fatalf("expected injected error, got %v", err)
	}
	if f.Call("Put") != nil {
		t.Fatal("non-matching method must be unaffected")
	}
	// Mutation: Call returning nil while applied fails the injected-error assertion.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if f.Call("Get") != nil {
		t.Fatal("post-restore must not inject")
	}
}

func TestRPCErrorCustom(t *testing.T) {
	sentinel := errors.New("boom")
	f := &RPCError{Method: "Get", Err: sentinel}
	_ = f.Apply()
	if !errors.Is(f.Call("Get"), sentinel) {
		t.Fatal("custom error must be returned")
	}
	// Mutation: ignoring f.Err and always returning ErrInjectedRPC fails this.
}

func TestRPCTimeout(t *testing.T) {
	f := &RPCTimeout{Method: "Get", Latency: 5 * time.Second}
	if f.WouldTimeout("Get", time.Second) {
		t.Fatal("baseline must not time out")
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	if !f.WouldTimeout("Get", time.Second) {
		t.Fatal("5s latency must exceed 1s deadline")
	}
	if f.WouldTimeout("Get", 10*time.Second) {
		t.Fatal("generous deadline must not time out")
	}
	if f.WouldTimeout("Put", time.Nanosecond) {
		t.Fatal("non-matching method must be unaffected")
	}
	// Mutation: WouldTimeout returning false always fails the exceed assertion.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if f.WouldTimeout("Get", time.Second) {
		t.Fatal("post-restore must not time out")
	}
}

func TestRPCMalformed(t *testing.T) {
	payload := []byte("0123456789")
	f := &RPCMalformed{Method: "Get"}
	if !bytes.Equal(f.Response("Get", payload), payload) {
		t.Fatal("baseline response intact")
	}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	got := f.Response("Get", payload)
	if len(got) != len(payload)/2 {
		t.Fatalf("expected truncated payload len %d, got %d", len(payload)/2, len(got))
	}
	if bytes.Equal(f.Response("Put", payload), payload) == false {
		t.Fatal("non-matching method must be intact")
	}
	// Mutation: Response returning payload unchanged while applied fails the truncation assertion.
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.Response("Get", payload), payload) {
		t.Fatal("post-restore response intact")
	}
}

// --- Contract / catalog ----------------------------------------------------

// TestAllNewFaultsContract drives the double-apply/double-restore guard over
// every new fault to prove the shared apply-state machine is wired up.
func TestAllNewFaultsContract(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	faults := []Fault{
		&PacketReorder{From: "a", To: "b", Window: 2},
		&PacketDuplication{From: "a", To: "b", Copies: 2},
		&BandwidthThrottle{From: "a", To: "b", BytesPerSec: 10},
		&AsymmetricPartition{From: "a", To: "b"},
		&DNSBlackhole{Hosts: map[string]bool{"x": true}},
		&ProcessPause{NodeID: "n"},
		&OOMKill{NodeID: "n", LimitB: 10},
		&SlowDisk{NodeID: "n", Factor: 2},
		&ClockSkew{NodeID: "n", Offset: time.Second},
		&ClockFreeze{NodeID: "n"},
		&ClockJump{NodeID: "n", Delta: time.Second},
		&CPUHog{NodeID: "n", Percent: 50},
		&MemPressure{NodeID: "n", TotalB: 100, Percent: 50},
		&FDExhaust{NodeID: "n", Limit: 1},
		NewDiskCorruption("n", rng),
		&DiskFull{NodeID: "n", CapacityB: 10},
		&ReadOnlyMount{NodeID: "n"},
		&FsyncLoss{NodeID: "n"},
		&RPCError{Method: "Get"},
		&RPCTimeout{Method: "Get", Latency: time.Second},
		&RPCMalformed{Method: "Get"},
	}
	for _, f := range faults {
		roundTrip(t, f)
	}
}

// TestFaultCatalogSize proves the catalog reaches the 25+ distinct-type target.
func TestFaultCatalogSize(t *testing.T) {
	cat := FaultCatalog(1)
	if len(cat) < 25 {
		t.Fatalf("expected at least 25 faults in catalog, got %d", len(cat))
	}
	seen := make(map[string]bool)
	for _, f := range cat {
		if seen[f.Name()] {
			t.Fatalf("duplicate fault name in catalog: %s", f.Name())
		}
		seen[f.Name()] = true
	}
	if len(seen) != len(cat) {
		t.Fatal("catalog names must be distinct")
	}
	// Mutation: removing entries from FaultCatalog below 25 fails the size gate.
}

// TestCatalogRoundTrip proves ApplyAll/RestoreAll over the whole catalog works
// and that a second ApplyAll fails (idempotency guard intact end-to-end).
func TestCatalogRoundTrip(t *testing.T) {
	cr := NewChaosRunner()
	for _, f := range FaultCatalog(2024) {
		cr.AddFault(f)
	}
	if err := cr.ApplyAll(); err != nil {
		t.Fatalf("ApplyAll: %v", err)
	}
	if err := cr.ApplyAll(); err == nil {
		t.Fatal("expected ApplyAll to fail when faults already applied")
	}
	if err := cr.RestoreAll(); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	if err := cr.RestoreAll(); err == nil {
		t.Fatal("expected RestoreAll to fail when faults already restored")
	}
	// Mutation: a fault whose Apply is a no-op (never sets applied) makes the
	// second ApplyAll succeed, failing the idempotency assertion.
}

// TestCatalogByCategory proves every category bucket is populated and that the
// network bucket reflects the additive Categorized interface.
func TestCatalogByCategory(t *testing.T) {
	groups := CatalogByCategory(5)
	wantNonEmpty := []Category{
		CategoryNetwork, CategoryProcess, CategoryClock,
		CategoryStorage, CategoryRPC, CategoryRuntime,
	}
	for _, c := range wantNonEmpty {
		if len(groups[c]) == 0 {
			t.Fatalf("category %q must have at least one fault", c)
		}
	}
	// The 5 existing fault.go types do not implement Categorized, so they land
	// in the uncategorized bucket. This proves backward compatibility.
	if len(groups[Category("uncategorized")]) != 5 {
		t.Fatalf("expected 5 uncategorized legacy faults, got %d", len(groups[Category("uncategorized")]))
	}
	// Mutation: making every fault implement Categorized empties the
	// uncategorized bucket and fails this count.
}

// TestCatalogDeterministicNames proves the catalog ordering/content is stable
// for a fixed seed (DST reproducibility at the catalog level).
func TestCatalogDeterministicNames(t *testing.T) {
	a := FaultCatalog(123)
	b := FaultCatalog(123)
	if len(a) != len(b) {
		t.Fatal("catalog length must be stable")
	}
	for i := range a {
		if a[i].Name() != b[i].Name() {
			t.Fatalf("catalog order diverged at %d: %s vs %s", i, a[i].Name(), b[i].Name())
		}
	}
	// Mutation: building the catalog from a map (random iteration order) makes
	// the per-index names diverge across calls, failing here.
}
