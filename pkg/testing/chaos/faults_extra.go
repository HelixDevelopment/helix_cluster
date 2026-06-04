package chaos

import (
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// Category groups faults by the subsystem they perturb. It is advisory metadata
// used by tooling/reporting; it does not affect Apply/Restore semantics.
type Category string

const (
	CategoryNetwork Category = "network"
	CategoryProcess Category = "process"
	CategoryClock   Category = "clock"
	CategoryStorage Category = "storage"
	CategoryRPC     Category = "rpc"
	CategoryRuntime Category = "runtime"
)

// Categorized is implemented by faults that expose a Category. Existing faults
// in fault.go do not implement it; this is purely additive and backward
// compatible.
type Categorized interface {
	Category() Category
}

// applyState is a small embeddable helper providing the standard
// already-applied / not-applied guarding shared by all faults so the observable
// behaviour is consistently gated on the applied flag.
type applyState struct {
	mu      sync.Mutex
	applied bool
}

func (s *applyState) apply(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applied {
		return fmt.Errorf("%s already applied: %w", name, ErrAlreadyApplied)
	}
	s.applied = true
	return nil
}

func (s *applyState) restore(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.applied {
		return fmt.Errorf("%s not applied: %w", name, ErrNotApplied)
	}
	s.applied = false
	return nil
}

func (s *applyState) isApplied() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applied
}

// ---------------------------------------------------------------------------
// Network faults
// ---------------------------------------------------------------------------

// PacketReorder buffers up to Window messages while applied and releases them in
// reverse arrival order, modelling out-of-order delivery on a link.
type PacketReorder struct {
	From, To string
	Window   int
	st       applyState
	buf      []int
}

// Name returns the fault name.
func (f *PacketReorder) Name() string { return "packet-reorder" }

// Category returns the fault category.
func (f *PacketReorder) Category() Category { return CategoryNetwork }

// Apply enables reordering. Restore disables it and flushes the buffer.
func (f *PacketReorder) Apply() error { return f.st.apply(f.Name()) }

// Restore disables reordering and clears any buffered sequence numbers.
func (f *PacketReorder) Restore() error {
	if err := f.st.restore(f.Name()); err != nil {
		return err
	}
	f.buf = nil
	return nil
}

// Deliver feeds a sequence number through the link and returns the sequence
// numbers that are released now (in delivery order). While applied, the most
// recent Window items are held back and emitted last-in-first-out so a strictly
// increasing input stream is observably reordered.
func (f *PacketReorder) Deliver(seq int) []int {
	if !f.st.isApplied() {
		return []int{seq}
	}
	w := f.Window
	if w < 1 {
		w = 1
	}
	f.buf = append(f.buf, seq)
	if len(f.buf) < w {
		return nil
	}
	out := make([]int, len(f.buf))
	for i, v := range f.buf {
		out[len(f.buf)-1-i] = v
	}
	f.buf = nil
	return out
}

// PacketDuplication emits each delivered message Copies times while applied.
type PacketDuplication struct {
	From, To string
	Copies   int
	st       applyState
}

// Name returns the fault name.
func (f *PacketDuplication) Name() string { return "packet-duplication" }

// Category returns the fault category.
func (f *PacketDuplication) Category() Category { return CategoryNetwork }

// Apply enables duplication.
func (f *PacketDuplication) Apply() error { return f.st.apply(f.Name()) }

// Restore disables duplication.
func (f *PacketDuplication) Restore() error { return f.st.restore(f.Name()) }

// DeliverCount returns how many copies of a single message are observed on the
// wire. Baseline (not applied) is exactly 1.
func (f *PacketDuplication) DeliverCount() int {
	if !f.st.isApplied() {
		return 1
	}
	if f.Copies < 1 {
		return 1
	}
	return f.Copies
}

// BandwidthThrottle caps the throughput of a link to BytesPerSec, returning the
// transmission time for a payload of a given size.
type BandwidthThrottle struct {
	From, To    string
	BytesPerSec int64
	st          applyState
}

// Name returns the fault name.
func (f *BandwidthThrottle) Name() string { return "bandwidth-throttle" }

// Category returns the fault category.
func (f *BandwidthThrottle) Category() Category { return CategoryNetwork }

// Apply enables throttling.
func (f *BandwidthThrottle) Apply() error { return f.st.apply(f.Name()) }

// Restore disables throttling.
func (f *BandwidthThrottle) Restore() error { return f.st.restore(f.Name()) }

// TransmitTime returns the simulated time to push nBytes across the link. When
// not applied the link is treated as instantaneous (zero).
func (f *BandwidthThrottle) TransmitTime(nBytes int64) time.Duration {
	if !f.st.isApplied() || f.BytesPerSec <= 0 {
		return 0
	}
	return time.Duration(float64(nBytes) / float64(f.BytesPerSec) * float64(time.Second))
}

// AsymmetricPartition drops traffic in one direction only (From -> To) while
// leaving the reverse path intact, modelling a half-open partition.
type AsymmetricPartition struct {
	From, To string
	st       applyState
}

// Name returns the fault name.
func (f *AsymmetricPartition) Name() string { return "asymmetric-partition" }

// Category returns the fault category.
func (f *AsymmetricPartition) Category() Category { return CategoryNetwork }

// Apply enables the one-way partition.
func (f *AsymmetricPartition) Apply() error { return f.st.apply(f.Name()) }

// Restore heals the partition.
func (f *AsymmetricPartition) Restore() error { return f.st.restore(f.Name()) }

// Blocked reports whether a message from src to dst is dropped.
func (f *AsymmetricPartition) Blocked(src, dst string) bool {
	if !f.st.isApplied() {
		return false
	}
	return src == f.From && dst == f.To
}

// DNSBlackhole resolves matching hostnames to an empty result while applied,
// modelling name-resolution failure.
type DNSBlackhole struct {
	Hosts map[string]bool
	st    applyState
}

// Name returns the fault name.
func (f *DNSBlackhole) Name() string { return "dns-blackhole" }

// Category returns the fault category.
func (f *DNSBlackhole) Category() Category { return CategoryNetwork }

// Apply enables the blackhole.
func (f *DNSBlackhole) Apply() error { return f.st.apply(f.Name()) }

// Restore disables the blackhole.
func (f *DNSBlackhole) Restore() error { return f.st.restore(f.Name()) }

// Resolve returns the resolved address and ok=false when the host is
// blackholed. When not applied every host resolves to a synthetic address.
func (f *DNSBlackhole) Resolve(host string) (addr string, ok bool) {
	if f.st.isApplied() && f.Hosts[host] {
		return "", false
	}
	return host + ".resolved", true
}

// ---------------------------------------------------------------------------
// Process faults
// ---------------------------------------------------------------------------

// ProcessPause models SIGSTOP: while applied the process makes no progress, so
// its logical clock does not advance across Tick calls.
type ProcessPause struct {
	NodeID string
	st     applyState
	ticks  int64
}

// Name returns the fault name.
func (f *ProcessPause) Name() string { return "process-pause" }

// Category returns the fault category.
func (f *ProcessPause) Category() Category { return CategoryProcess }

// Apply pauses the process.
func (f *ProcessPause) Apply() error { return f.st.apply(f.Name()) }

// Restore resumes the process.
func (f *ProcessPause) Restore() error { return f.st.restore(f.Name()) }

// Tick attempts to advance the process clock by one and returns the new value.
// While paused the value does not change.
func (f *ProcessPause) Tick() int64 {
	if f.st.isApplied() {
		return f.ticks
	}
	f.ticks++
	return f.ticks
}

// Ticks returns the current progress counter.
func (f *ProcessPause) Ticks() int64 { return f.ticks }

// OOMKill models the kernel OOM killer: once committed memory exceeds LimitBytes
// while applied, allocation is refused.
type OOMKill struct {
	NodeID    string
	LimitB    int64
	st        applyState
	committed int64
}

// Name returns the fault name.
func (f *OOMKill) Name() string { return "oom-kill" }

// Category returns the fault category.
func (f *OOMKill) Category() Category { return CategoryProcess }

// Apply arms the OOM limit, measuring committed memory from zero.
func (f *OOMKill) Apply() error {
	if err := f.st.apply(f.Name()); err != nil {
		return err
	}
	f.committed = 0
	return nil
}

// Restore disarms the OOM limit and resets committed memory.
func (f *OOMKill) Restore() error {
	if err := f.st.restore(f.Name()); err != nil {
		return err
	}
	f.committed = 0
	return nil
}

// Alloc requests nBytes. It returns false (allocation denied) when the limit is
// armed and would be exceeded. When not applied allocation always succeeds and
// no budget is tracked (the limit is measured from the moment Apply arms it).
func (f *OOMKill) Alloc(nBytes int64) bool {
	if !f.st.isApplied() {
		return true
	}
	if f.committed+nBytes > f.LimitB {
		return false
	}
	f.committed += nBytes
	return true
}

// SlowDisk multiplies I/O latency by Factor while applied.
type SlowDisk struct {
	NodeID string
	Factor float64
	st     applyState
}

// Name returns the fault name.
func (f *SlowDisk) Name() string { return "slow-disk" }

// Category returns the fault category.
func (f *SlowDisk) Category() Category { return CategoryProcess }

// Apply enables the slowdown.
func (f *SlowDisk) Apply() error { return f.st.apply(f.Name()) }

// Restore disables the slowdown.
func (f *SlowDisk) Restore() error { return f.st.restore(f.Name()) }

// IOTime returns the effective time for an I/O whose baseline cost is base.
func (f *SlowDisk) IOTime(base time.Duration) time.Duration {
	if !f.st.isApplied() || f.Factor <= 0 {
		return base
	}
	return time.Duration(float64(base) * f.Factor)
}

// ---------------------------------------------------------------------------
// Clock faults
// ---------------------------------------------------------------------------

// ClockSkew shifts observed time by a fixed Offset (which may be negative).
type ClockSkew struct {
	NodeID string
	Offset time.Duration
	st     applyState
}

// Name returns the fault name.
func (f *ClockSkew) Name() string { return "clock-skew" }

// Category returns the fault category.
func (f *ClockSkew) Category() Category { return CategoryClock }

// Apply enables the skew.
func (f *ClockSkew) Apply() error { return f.st.apply(f.Name()) }

// Restore removes the skew.
func (f *ClockSkew) Restore() error { return f.st.restore(f.Name()) }

// Now returns the perturbed view of base.
func (f *ClockSkew) Now(base time.Time) time.Time {
	if !f.st.isApplied() {
		return base
	}
	return base.Add(f.Offset)
}

// ClockFreeze pins observed time to the moment of the first Now call after Apply.
type ClockFreeze struct {
	NodeID string
	st     applyState
	frozen time.Time
	set    bool
}

// Name returns the fault name.
func (f *ClockFreeze) Name() string { return "clock-freeze" }

// Category returns the fault category.
func (f *ClockFreeze) Category() Category { return CategoryClock }

// Apply freezes the clock.
func (f *ClockFreeze) Apply() error { return f.st.apply(f.Name()) }

// Restore unfreezes the clock.
func (f *ClockFreeze) Restore() error {
	if err := f.st.restore(f.Name()); err != nil {
		return err
	}
	f.set = false
	return nil
}

// Now returns the frozen instant while applied; otherwise it passes base through.
func (f *ClockFreeze) Now(base time.Time) time.Time {
	if !f.st.isApplied() {
		return base
	}
	if !f.set {
		f.frozen = base
		f.set = true
	}
	return f.frozen
}

// ClockJump adds a one-shot discontinuity of Delta the first time Now is called
// after Apply, then keeps applying the same delta (modelling an NTP step).
type ClockJump struct {
	NodeID string
	Delta  time.Duration
	st     applyState
}

// Name returns the fault name.
func (f *ClockJump) Name() string { return "clock-jump" }

// Category returns the fault category.
func (f *ClockJump) Category() Category { return CategoryClock }

// Apply arms the jump.
func (f *ClockJump) Apply() error { return f.st.apply(f.Name()) }

// Restore disarms the jump.
func (f *ClockJump) Restore() error { return f.st.restore(f.Name()) }

// Now returns base shifted by Delta while applied.
func (f *ClockJump) Now(base time.Time) time.Time {
	if !f.st.isApplied() {
		return base
	}
	return base.Add(f.Delta)
}

// ---------------------------------------------------------------------------
// Runtime / resource faults
// ---------------------------------------------------------------------------

// CPUHog steals a fraction of CPU, stretching the wall-clock time of work.
type CPUHog struct {
	NodeID  string
	Percent float64 // 0..100 stolen
	st      applyState
}

// Name returns the fault name.
func (f *CPUHog) Name() string { return "cpu-hog" }

// Category returns the fault category.
func (f *CPUHog) Category() Category { return CategoryRuntime }

// Apply enables the hog.
func (f *CPUHog) Apply() error { return f.st.apply(f.Name()) }

// Restore disables the hog.
func (f *CPUHog) Restore() error { return f.st.restore(f.Name()) }

// WorkTime returns the stretched duration for work that nominally takes base.
func (f *CPUHog) WorkTime(base time.Duration) time.Duration {
	if !f.st.isApplied() {
		return base
	}
	avail := 1.0 - f.Percent/100.0
	if avail <= 0 {
		avail = 0.0001
	}
	return time.Duration(float64(base) / avail)
}

// MemPressure reclaims a fraction of available memory, shrinking the headroom
// reported to callers.
type MemPressure struct {
	NodeID  string
	TotalB  int64
	Percent float64 // fraction consumed by pressure
	st      applyState
}

// Name returns the fault name.
func (f *MemPressure) Name() string { return "mem-pressure" }

// Category returns the fault category.
func (f *MemPressure) Category() Category { return CategoryRuntime }

// Apply enables the pressure.
func (f *MemPressure) Apply() error { return f.st.apply(f.Name()) }

// Restore relieves the pressure.
func (f *MemPressure) Restore() error { return f.st.restore(f.Name()) }

// Available returns the free memory headroom.
func (f *MemPressure) Available() int64 {
	if !f.st.isApplied() {
		return f.TotalB
	}
	consumed := int64(float64(f.TotalB) * f.Percent / 100.0)
	return f.TotalB - consumed
}

// FDExhaust caps the number of file descriptors that can be opened.
type FDExhaust struct {
	NodeID string
	Limit  int
	st     applyState
	open   int
}

// Name returns the fault name.
func (f *FDExhaust) Name() string { return "fd-exhaust" }

// Category returns the fault category.
func (f *FDExhaust) Category() Category { return CategoryRuntime }

// Apply arms the descriptor cap, counting from zero open descriptors.
func (f *FDExhaust) Apply() error {
	if err := f.st.apply(f.Name()); err != nil {
		return err
	}
	f.open = 0
	return nil
}

// Restore disarms the cap and resets the counter.
func (f *FDExhaust) Restore() error {
	if err := f.st.restore(f.Name()); err != nil {
		return err
	}
	f.open = 0
	return nil
}

// OpenFD attempts to open a descriptor, returning false when the cap is hit.
// When not applied opens always succeed and none are tracked; the cap is
// measured from the moment Apply arms it.
func (f *FDExhaust) OpenFD() bool {
	if !f.st.isApplied() {
		return true
	}
	if f.open >= f.Limit {
		return false
	}
	f.open++
	return true
}

// ---------------------------------------------------------------------------
// Storage faults
// ---------------------------------------------------------------------------

// DiskCorruption flips one byte at a seeded position in data read back from disk.
type DiskCorruption struct {
	NodeID string
	rng    *rand.Rand
	st     applyState
}

// NewDiskCorruption creates a disk-corruption fault with a seeded PRNG.
func NewDiskCorruption(nodeID string, rng *rand.Rand) *DiskCorruption {
	return &DiskCorruption{NodeID: nodeID, rng: rng}
}

// Name returns the fault name.
func (f *DiskCorruption) Name() string { return "disk-corruption" }

// Category returns the fault category.
func (f *DiskCorruption) Category() Category { return CategoryStorage }

// Apply enables corruption.
func (f *DiskCorruption) Apply() error { return f.st.apply(f.Name()) }

// Restore disables corruption.
func (f *DiskCorruption) Restore() error { return f.st.restore(f.Name()) }

// Read returns a possibly-corrupted copy of data. While applied exactly one
// byte is flipped at a deterministic position; otherwise the data is returned
// verbatim.
func (f *DiskCorruption) Read(data []byte) []byte {
	out := make([]byte, len(data))
	copy(out, data)
	if !f.st.isApplied() || len(out) == 0 {
		return out
	}
	pos := f.rng.Intn(len(out))
	out[pos] ^= 0xFF
	return out
}

// DiskFull rejects writes once used space would exceed CapacityB.
type DiskFull struct {
	NodeID    string
	CapacityB int64
	st        applyState
	used      int64
}

// Name returns the fault name.
func (f *DiskFull) Name() string { return "disk-full" }

// Category returns the fault category.
func (f *DiskFull) Category() Category { return CategoryStorage }

// Apply arms the capacity limit, measuring usage from zero.
func (f *DiskFull) Apply() error {
	if err := f.st.apply(f.Name()); err != nil {
		return err
	}
	f.used = 0
	return nil
}

// Restore disarms the limit and resets usage.
func (f *DiskFull) Restore() error {
	if err := f.st.restore(f.Name()); err != nil {
		return err
	}
	f.used = 0
	return nil
}

// Write attempts to persist nBytes, returning false when the disk is full.
// When not applied writes always succeed and no usage is tracked; the capacity
// is measured from the moment Apply arms it.
func (f *DiskFull) Write(nBytes int64) bool {
	if !f.st.isApplied() {
		return true
	}
	if f.used+nBytes > f.CapacityB {
		return false
	}
	f.used += nBytes
	return true
}

// ReadOnlyMount rejects writes while permitting reads.
type ReadOnlyMount struct {
	NodeID string
	st     applyState
}

// Name returns the fault name.
func (f *ReadOnlyMount) Name() string { return "readonly-mount" }

// Category returns the fault category.
func (f *ReadOnlyMount) Category() Category { return CategoryStorage }

// Apply remounts read-only.
func (f *ReadOnlyMount) Apply() error { return f.st.apply(f.Name()) }

// Restore remounts read-write.
func (f *ReadOnlyMount) Restore() error { return f.st.restore(f.Name()) }

// CanWrite reports whether a write would be permitted.
func (f *ReadOnlyMount) CanWrite() bool { return !f.st.isApplied() }

// FsyncLoss models a power-loss window: writes Apply()ed but not yet flushed are
// lost. While applied, Write buffers data that Sync silently discards.
type FsyncLoss struct {
	NodeID  string
	st      applyState
	dirty   int64
	durable int64
}

// Name returns the fault name.
func (f *FsyncLoss) Name() string { return "fsync-loss" }

// Category returns the fault category.
func (f *FsyncLoss) Category() Category { return CategoryStorage }

// Apply enables fsync loss.
func (f *FsyncLoss) Apply() error { return f.st.apply(f.Name()) }

// Restore disables fsync loss.
func (f *FsyncLoss) Restore() error { return f.st.restore(f.Name()) }

// Write records nBytes as dirty (unflushed) data.
func (f *FsyncLoss) Write(nBytes int64) { f.dirty += nBytes }

// Sync flushes dirty data. While applied the flush is a no-op (data lost);
// otherwise dirty data becomes durable. Returns durable byte count.
func (f *FsyncLoss) Sync() int64 {
	if f.st.isApplied() {
		f.dirty = 0
		return f.durable
	}
	f.durable += f.dirty
	f.dirty = 0
	return f.durable
}

// Durable returns the number of bytes that survived flushing.
func (f *FsyncLoss) Durable() int64 { return f.durable }

// ---------------------------------------------------------------------------
// RPC faults
// ---------------------------------------------------------------------------

// RPCError injects an error into matching RPC method calls.
type RPCError struct {
	Method string
	Err    error
	st     applyState
}

// Name returns the fault name.
func (f *RPCError) Name() string { return "rpc-error" }

// Category returns the fault category.
func (f *RPCError) Category() Category { return CategoryRPC }

// Apply enables the injection.
func (f *RPCError) Apply() error { return f.st.apply(f.Name()) }

// Restore disables the injection.
func (f *RPCError) Restore() error { return f.st.restore(f.Name()) }

// Call returns the injected error for the matching method while applied.
func (f *RPCError) Call(method string) error {
	if !f.st.isApplied() || method != f.Method {
		return nil
	}
	if f.Err != nil {
		return f.Err
	}
	return fmt.Errorf("rpc %s: %w", method, ErrInjectedRPC)
}

// ErrInjectedRPC is the default error returned by RPCError when no explicit
// error is configured.
var ErrInjectedRPC = fmt.Errorf("injected rpc failure")

// RPCTimeout forces matching RPC calls to exceed the deadline.
type RPCTimeout struct {
	Method  string
	Latency time.Duration
	st      applyState
}

// Name returns the fault name.
func (f *RPCTimeout) Name() string { return "rpc-timeout" }

// Category returns the fault category.
func (f *RPCTimeout) Category() Category { return CategoryRPC }

// Apply enables the timeout injection.
func (f *RPCTimeout) Apply() error { return f.st.apply(f.Name()) }

// Restore disables the timeout injection.
func (f *RPCTimeout) Restore() error { return f.st.restore(f.Name()) }

// WouldTimeout reports whether a call to method with the given deadline would
// time out under the injected latency.
func (f *RPCTimeout) WouldTimeout(method string, deadline time.Duration) bool {
	if !f.st.isApplied() || method != f.Method {
		return false
	}
	return f.Latency > deadline
}

// RPCMalformed corrupts the response payload of matching RPC calls.
type RPCMalformed struct {
	Method string
	st     applyState
}

// Name returns the fault name.
func (f *RPCMalformed) Name() string { return "rpc-malformed" }

// Category returns the fault category.
func (f *RPCMalformed) Category() Category { return CategoryRPC }

// Apply enables payload corruption.
func (f *RPCMalformed) Apply() error { return f.st.apply(f.Name()) }

// Restore disables payload corruption.
func (f *RPCMalformed) Restore() error { return f.st.restore(f.Name()) }

// Response returns the payload that the caller observes. While applied, matching
// methods receive a truncated (malformed) payload; otherwise the payload is
// returned intact.
func (f *RPCMalformed) Response(method string, payload []byte) []byte {
	if !f.st.isApplied() || method != f.Method {
		return payload
	}
	// Truncate to half length to produce an undecodable response.
	return payload[:len(payload)/2]
}

// ---------------------------------------------------------------------------
// Catalog helpers
// ---------------------------------------------------------------------------

// FaultCatalog returns a deterministic, fully-constructed instance of every
// fault type in the package, suitable for round-trip ApplyAll/RestoreAll
// testing. The provided seed makes any randomized faults reproducible.
func FaultCatalog(seed int64) []Fault {
	rng := rand.New(rand.NewSource(seed)) //gosec:disable G404 -- deterministic chaos fault catalog requires a reproducible seeded PRNG, not crypto/rand
	faults := []Fault{
		// Existing (fault.go) types.
		&NetworkPartition{Nodes: []string{"a", "b"}},
		NewPacketLoss("a", "b", 50.0, rand.New(rand.NewSource(seed+1))),                                            //gosec:disable G404 -- deterministic chaos fault requires a reproducible seeded PRNG, not crypto/rand
		NewLatencyInjection("a", "b", 100*time.Millisecond, 10*time.Millisecond, rand.New(rand.NewSource(seed+2))), //gosec:disable G404 -- deterministic chaos fault requires a reproducible seeded PRNG, not crypto/rand
		&NodeCrash{NodeID: "n1"},
		&ResourceExhaustion{NodeID: "n1", Resource: "cpu", Percent: 95.0},
		// Network.
		&PacketReorder{From: "a", To: "b", Window: 3},
		&PacketDuplication{From: "a", To: "b", Copies: 2},
		&BandwidthThrottle{From: "a", To: "b", BytesPerSec: 1024},
		&AsymmetricPartition{From: "a", To: "b"},
		&DNSBlackhole{Hosts: map[string]bool{"db": true}},
		// Process.
		&ProcessPause{NodeID: "n1"},
		&OOMKill{NodeID: "n1", LimitB: 1024},
		&SlowDisk{NodeID: "n1", Factor: 10},
		// Clock.
		&ClockSkew{NodeID: "n1", Offset: time.Second},
		&ClockFreeze{NodeID: "n1"},
		&ClockJump{NodeID: "n1", Delta: time.Hour},
		// Runtime.
		&CPUHog{NodeID: "n1", Percent: 80},
		&MemPressure{NodeID: "n1", TotalB: 1000, Percent: 90},
		&FDExhaust{NodeID: "n1", Limit: 2},
		// Storage.
		NewDiskCorruption("n1", rng),
		&DiskFull{NodeID: "n1", CapacityB: 1024},
		&ReadOnlyMount{NodeID: "n1"},
		&FsyncLoss{NodeID: "n1"},
		// RPC.
		&RPCError{Method: "Get"},
		&RPCTimeout{Method: "Get", Latency: time.Second},
		&RPCMalformed{Method: "Get"},
	}
	return faults
}

// CatalogByCategory groups the catalog by Category for reporting. Faults that do
// not implement Categorized are bucketed under "uncategorized".
func CatalogByCategory(seed int64) map[Category][]string {
	out := make(map[Category][]string)
	for _, f := range FaultCatalog(seed) {
		cat := Category("uncategorized")
		if c, ok := f.(Categorized); ok {
			cat = c.Category()
		}
		out[cat] = append(out[cat], f.Name())
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}
