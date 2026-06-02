package aws

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/pool"
)

// fakeEC2 is an in-memory EC2Client that records every call. It is the REAL
// seam (the same interface production's SDK-backed client implements), exercised
// in-process — not a mock that fakes a cloud PASS. Tests assert on the recorded
// requests (instance type, Spot options, tags) and on the live state it holds.
type fakeEC2 struct {
	mu sync.Mutex

	runCalls       []RunInstancesInput
	terminateCalls []TerminateInstancesInput
	describeCalls  int

	// nextID is the instance id handed back from the next RunInstances; if empty
	// an auto-incrementing id is used.
	nextID string
	seq    int
	// runErr, when set, makes RunInstances fail (provisioning-failure path).
	runErr error
	// terminateErr, when set, makes TerminateInstances fail.
	terminateErr error

	// live tracks instances the fake currently holds, keyed by id.
	live map[string]DescribedInstance
}

func newFakeEC2() *fakeEC2 { return &fakeEC2{live: make(map[string]DescribedInstance)} }

func (f *fakeEC2) RunInstances(ctx context.Context, in RunInstancesInput) (RunInstancesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runCalls = append(f.runCalls, in)
	if f.runErr != nil {
		return RunInstancesOutput{}, f.runErr
	}
	id := f.nextID
	if id == "" {
		f.seq++
		id = "i-" + string(rune('a'+f.seq-1))
	}
	f.live[id] = DescribedInstance{
		InstanceID:   id,
		InstanceType: in.InstanceType,
		Tags:         in.Tags,
	}
	return RunInstancesOutput{InstanceID: id}, nil
}

func (f *fakeEC2) TerminateInstances(ctx context.Context, in TerminateInstancesInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminateCalls = append(f.terminateCalls, in)
	if f.terminateErr != nil {
		return f.terminateErr
	}
	delete(f.live, in.InstanceID)
	return nil
}

func (f *fakeEC2) DescribeInstances(ctx context.Context, _ DescribeInstancesInput) (DescribeInstancesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.describeCalls++
	out := DescribeInstancesOutput{}
	for _, di := range f.live {
		out.Instances = append(out.Instances, di)
	}
	return out, nil
}

func (f *fakeEC2) lastRun() RunInstancesInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runCalls[len(f.runCalls)-1]
}

// Closure #1: AWSProvider satisfies pool.GPUProvider. The var line below is a
// compile-time assertion; this test also proves it is usable through the
// interface value (not just assignable).
func TestSatisfiesGPUProviderInterface(t *testing.T) {
	var _ pool.GPUProvider = (*AWSProvider)(nil)

	p, err := NewAWSProvider(Config{Client: newFakeEC2(), MaxInstances: 1})
	if err != nil {
		t.Fatalf("NewAWSProvider: %v", err)
	}
	var iface pool.GPUProvider = p
	if got := iface.Capacity(); got != 1 {
		t.Fatalf("Capacity via interface = %d, want 1", got)
	}
}

// Closure #2 + mutation guard: model -> instance-type selection. Asserts the
// type passed to the fake client. Breaking the gpuModelToInstanceType mapping
// (e.g. pointing H100 at the A100 type, or dropping an entry) makes the wantType
// assertion fail here.
func TestProvisionSelectsInstanceTypeByGPUModel(t *testing.T) {
	cases := []struct {
		name     string
		gpuModel string
		wantType string
	}{
		{name: "H100 picks p5", gpuModel: "H100", wantType: "p5.48xlarge"},
		{name: "A100 picks p4de", gpuModel: "A100", wantType: "p4de.24xlarge"},
		{name: "A10G picks g5", gpuModel: "A10G", wantType: "g5.xlarge"},
		{name: "lower-case model still resolves", gpuModel: "h100", wantType: "p5.48xlarge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeEC2()
			p, err := NewAWSProvider(Config{Client: fake, ClusterName: "helix", MaxInstances: 4})
			if err != nil {
				t.Fatalf("NewAWSProvider: %v", err)
			}
			inst, err := p.Provision(context.Background(), pool.Spec{GPUType: tc.gpuModel, HourlyUSD: 12.5})
			if err != nil {
				t.Fatalf("Provision(%s): %v", tc.gpuModel, err)
			}
			if len(fake.runCalls) != 1 {
				t.Fatalf("RunInstances calls = %d, want 1", len(fake.runCalls))
			}
			if got := fake.lastRun().InstanceType; got != tc.wantType {
				t.Fatalf("instance type passed to EC2 = %q, want %q", got, tc.wantType)
			}
			// Sink-side: the returned instance id is the one the backend assigned.
			if inst.ID == "" {
				t.Fatalf("returned instance has empty ID")
			}
			if inst.GPUType != tc.gpuModel {
				t.Fatalf("returned GPUType = %q, want %q", inst.GPUType, tc.gpuModel)
			}
		})
	}
}

// Closure #2 (negative): an unknown model errors with ErrUnknownGPUModel and
// issues NO billable RunInstances call.
func TestProvisionUnknownModelErrors(t *testing.T) {
	fake := newFakeEC2()
	p, err := NewAWSProvider(Config{Client: fake, MaxInstances: 1})
	if err != nil {
		t.Fatalf("NewAWSProvider: %v", err)
	}
	_, err = p.Provision(context.Background(), pool.Spec{GPUType: "V100"})
	if !errors.Is(err, ErrUnknownGPUModel) {
		t.Fatalf("Provision(V100) err = %v, want ErrUnknownGPUModel", err)
	}
	if len(fake.runCalls) != 0 {
		t.Fatalf("RunInstances was called %d times for an unknown model; want 0", len(fake.runCalls))
	}
}

// Closure #3 (Spot + tags): Provision issues a Spot RunInstances tagged with the
// cluster and gpu-model. Asserts the recorded request's Spot market type and
// tag values.
func TestProvisionIssuesSpotLaunchWithTags(t *testing.T) {
	fake := newFakeEC2()
	p, err := NewAWSProvider(Config{
		Client:                 fake,
		ClusterName:            "prod-east",
		MaxInstances:           2,
		MaxSpotPriceUSDPerHour: "9.99",
	})
	if err != nil {
		t.Fatalf("NewAWSProvider: %v", err)
	}
	if _, err := p.Provision(context.Background(), pool.Spec{GPUType: "A100"}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	req := fake.lastRun()
	if req.SpotOptions.MarketType != "spot" {
		t.Fatalf("market type = %q, want \"spot\" (must be a Spot launch)", req.SpotOptions.MarketType)
	}
	if req.SpotOptions.MaxPriceUSDPerHour != "9.99" {
		t.Fatalf("max spot price = %q, want \"9.99\"", req.SpotOptions.MaxPriceUSDPerHour)
	}
	if req.Tags[TagKeyCluster] != "prod-east" {
		t.Fatalf("cluster tag = %q, want \"prod-east\"", req.Tags[TagKeyCluster])
	}
	if req.Tags[TagKeyGPUModel] != "A100" {
		t.Fatalf("gpu-model tag = %q, want \"A100\"", req.Tags[TagKeyGPUModel])
	}
	if req.Tags[TagKeyName] == "" {
		t.Fatalf("Name tag is empty; want a non-empty name")
	}
}

// Closure #3 (Release): Release issues a TerminateInstances for the launched id
// and the backend no longer reports it live.
func TestReleaseTerminatesInstance(t *testing.T) {
	fake := newFakeEC2()
	p, err := NewAWSProvider(Config{Client: fake, ClusterName: "c", MaxInstances: 1})
	if err != nil {
		t.Fatalf("NewAWSProvider: %v", err)
	}
	inst, err := p.Provision(context.Background(), pool.Spec{GPUType: "A10G"})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(fake.live) != 1 {
		t.Fatalf("backend live = %d after provision, want 1", len(fake.live))
	}
	if err := p.Release(context.Background(), inst); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if len(fake.terminateCalls) != 1 {
		t.Fatalf("TerminateInstances calls = %d, want 1", len(fake.terminateCalls))
	}
	if fake.terminateCalls[0].InstanceID != inst.ID {
		t.Fatalf("terminated id = %q, want %q", fake.terminateCalls[0].InstanceID, inst.ID)
	}
	// Sink-side: the backend no longer holds it.
	if len(fake.live) != 0 {
		t.Fatalf("backend live = %d after release, want 0", len(fake.live))
	}
}

// Release with an empty id is rejected before any backend call.
func TestReleaseEmptyIDRejected(t *testing.T) {
	fake := newFakeEC2()
	p, _ := NewAWSProvider(Config{Client: fake, MaxInstances: 1})
	if err := p.Release(context.Background(), pool.Instance{}); !errors.Is(err, ErrEmptyInstanceID) {
		t.Fatalf("Release(empty) err = %v, want ErrEmptyInstanceID", err)
	}
	if len(fake.terminateCalls) != 0 {
		t.Fatalf("TerminateInstances called %d times for empty id; want 0", len(fake.terminateCalls))
	}
}

// End-to-end through the pool.GPUProvider seam: List reports live instances with
// the gpu-model read back from EC2 tags, and Capacity is enforced.
func TestListReflectsLiveInstancesAndCapacity(t *testing.T) {
	fake := newFakeEC2()
	var p pool.GPUProvider
	ap, err := NewAWSProvider(Config{Client: fake, ClusterName: "k", MaxInstances: 2})
	if err != nil {
		t.Fatalf("NewAWSProvider: %v", err)
	}
	p = ap

	a, err := p.Provision(context.Background(), pool.Spec{GPUType: "H100"})
	if err != nil {
		t.Fatalf("Provision H100: %v", err)
	}
	if _, err := p.Provision(context.Background(), pool.Spec{GPUType: "A100"}); err != nil {
		t.Fatalf("Provision A100: %v", err)
	}
	// Third provision must fail: capacity is 2.
	if _, err := p.Provision(context.Background(), pool.Spec{GPUType: "A10G"}); err == nil {
		t.Fatalf("Provision beyond capacity succeeded; want error")
	}

	live, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(live) != 2 {
		t.Fatalf("List returned %d instances, want 2", len(live))
	}
	// The gpu-model must be read back from the EC2 tag, not invented.
	var sawH100 bool
	for _, in := range live {
		if in.ID == a.ID {
			if in.GPUType != "H100" {
				t.Fatalf("listed gpu model for %q = %q, want H100", a.ID, in.GPUType)
			}
			sawH100 = true
		}
	}
	if !sawH100 {
		t.Fatalf("provisioned H100 instance %q not present in List", a.ID)
	}
}

// Provisioning failure is surfaced honestly (wrapped backend error), and no
// instance is tracked.
func TestProvisionSurfacesBackendError(t *testing.T) {
	fake := newFakeEC2()
	wire := errors.New("InsufficientInstanceCapacity")
	fake.runErr = wire
	p, _ := NewAWSProvider(Config{Client: fake, MaxInstances: 1})
	_, err := p.Provision(context.Background(), pool.Spec{GPUType: "H100"})
	if err == nil {
		t.Fatalf("Provision succeeded despite backend failure")
	}
	if !errors.Is(err, wire) {
		t.Fatalf("Provision err = %v, want it to wrap %v", err, wire)
	}
	p.mu.Lock()
	n := len(p.launched)
	p.mu.Unlock()
	if n != 0 {
		t.Fatalf("tracked %d instances after a failed provision; want 0", n)
	}
}

// A 2xx-equivalent launch that reports no instance id is treated as a broken
// contract, not a usable lease.
func TestProvisionEmptyInstanceIDIsError(t *testing.T) {
	p, _ := NewAWSProvider(Config{Client: &emptyIDClient{}, MaxInstances: 1})
	_, err := p.Provision(context.Background(), pool.Spec{GPUType: "H100"})
	if !errors.Is(err, ErrNoInstanceLaunched) {
		t.Fatalf("Provision err = %v, want ErrNoInstanceLaunched", err)
	}
}

// emptyIDClient always returns a launch with an empty instance id.
type emptyIDClient struct{}

func (emptyIDClient) RunInstances(context.Context, RunInstancesInput) (RunInstancesOutput, error) {
	return RunInstancesOutput{InstanceID: ""}, nil
}
func (emptyIDClient) TerminateInstances(context.Context, TerminateInstancesInput) error { return nil }
func (emptyIDClient) DescribeInstances(context.Context, DescribeInstancesInput) (DescribeInstancesOutput, error) {
	return DescribeInstancesOutput{}, nil
}

// barrierEC2 is an EC2Client whose RunInstances blocks until released, so a
// test can force N concurrent Provision calls to ALL be in-flight (past the
// Capacity gate) at the same instant — the exact window where a TOCTOU gate
// that does not reserve a slot would let every goroutine launch and overrun the
// quota. It records the launch count atomically.
type barrierEC2 struct {
	release  chan struct{} // closed to let all blocked RunInstances proceed
	entered  chan struct{} // one send per goroutine that has entered RunInstances
	mu       sync.Mutex
	runCount int
	seq      int
}

func newBarrierEC2() *barrierEC2 {
	return &barrierEC2{release: make(chan struct{}), entered: make(chan struct{}, 1024)}
}

func (b *barrierEC2) RunInstances(ctx context.Context, in RunInstancesInput) (RunInstancesOutput, error) {
	b.entered <- struct{}{} // announce arrival inside the billable call
	<-b.release             // hold here until the test releases the barrier
	b.mu.Lock()
	b.runCount++
	b.seq++
	id := "i-bar-" + string(rune('a'+b.seq-1))
	b.mu.Unlock()
	return RunInstancesOutput{InstanceID: id}, nil
}

func (b *barrierEC2) TerminateInstances(context.Context, TerminateInstancesInput) error { return nil }
func (b *barrierEC2) DescribeInstances(context.Context, DescribeInstancesInput) (DescribeInstancesOutput, error) {
	return DescribeInstancesOutput{}, nil
}

// TestProvisionCapacityIsConcurrencySafe is the mutation guard for the TOCTOU
// fix. With Capacity 1 and 4 concurrent Provision calls all blocked INSIDE the
// (billable) RunInstances at once, exactly one may pass the gate and launch.
// If the gate is mutated back to `len(p.launched) >= p.capacity` (ignoring the
// reserved count), all 4 goroutines pass the gate before any inserts into
// launched, all 4 enter RunInstances, and this test fails: it asserts that no
// more than capacity goroutines ever reach the billable call.
func TestProvisionCapacityIsConcurrencySafe(t *testing.T) {
	const capacity = 1
	const goroutines = 4
	bar := newBarrierEC2()
	p, err := NewAWSProvider(Config{Client: bar, ClusterName: "race", MaxInstances: capacity})
	if err != nil {
		t.Fatalf("NewAWSProvider: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			_, e := p.Provision(context.Background(), pool.Spec{GPUType: "H100"})
			errs[idx] = e
		}(i)
	}

	// Wait until exactly `capacity` goroutines have entered the billable call,
	// then give the rest a moment to (correctly) be rejected at the gate before
	// releasing the barrier. We read `entered` capacity times — there must be at
	// least that many — and then assert no MORE arrive within a grace window.
	for i := 0; i < capacity; i++ {
		select {
		case <-bar.entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d goroutine(s) reached RunInstances; want %d", i, capacity)
		}
	}
	// Grace window: if the gate were broken, the extra goroutines would also be
	// blocked in RunInstances and would show up on `entered` here.
	select {
	case <-bar.entered:
		t.Fatalf("more than capacity=%d goroutines reached the billable RunInstances; capacity gate overrun (TOCTOU)", capacity)
	case <-time.After(200 * time.Millisecond):
		// good: the surplus goroutines were rejected at the gate, not launched.
	}

	close(bar.release)
	wg.Wait()

	bar.mu.Lock()
	gotRuns := bar.runCount
	bar.mu.Unlock()
	if gotRuns != capacity {
		t.Fatalf("RunInstances issued %d times; want exactly capacity=%d (overrun launches real billable GPUs)", gotRuns, capacity)
	}

	var ok, failed int
	for _, e := range errs {
		if e == nil {
			ok++
		} else {
			failed++
		}
	}
	if ok != capacity {
		t.Fatalf("%d Provision calls succeeded; want exactly capacity=%d", ok, capacity)
	}
	if failed != goroutines-capacity {
		t.Fatalf("%d Provision calls failed; want %d (the over-capacity ones)", failed, goroutines-capacity)
	}
}

// TestProvisionFailureReleasesReservation proves the reservation is rolled back
// on a launch failure: a provider at capacity 1 whose first Provision fails must
// still allow a subsequent Provision to launch (the failed attempt must not
// permanently consume the single slot). If the rollback were dropped, the second
// Provision would be wrongly rejected with "at capacity".
func TestProvisionFailureReleasesReservation(t *testing.T) {
	fake := newFakeEC2()
	wire := errors.New("InsufficientInstanceCapacity")
	fake.runErr = wire
	p, err := NewAWSProvider(Config{Client: fake, ClusterName: "c", MaxInstances: 1})
	if err != nil {
		t.Fatalf("NewAWSProvider: %v", err)
	}
	if _, err := p.Provision(context.Background(), pool.Spec{GPUType: "H100"}); !errors.Is(err, wire) {
		t.Fatalf("first Provision err = %v, want wrapped %v", err, wire)
	}
	// The reserved slot must have been released; clear the error and retry.
	fake.mu.Lock()
	fake.runErr = nil
	fake.mu.Unlock()
	inst, err := p.Provision(context.Background(), pool.Spec{GPUType: "H100"})
	if err != nil {
		t.Fatalf("second Provision after a failed one: %v (reservation not rolled back?)", err)
	}
	if inst.ID == "" {
		t.Fatalf("second Provision returned empty ID")
	}
	// seq must be gap-free: the failed attempt rolled back seq, so the only live
	// launch carries the -0001 name.
	if got := fake.lastRun().Tags[TagKeyName]; got != "c-gpu-0001" {
		t.Fatalf("Name tag = %q, want \"c-gpu-0001\" (seq should roll back on failure)", got)
	}
}

// NewAWSProvider rejects a nil client rather than silently no-opping.
func TestNewAWSProviderRequiresClient(t *testing.T) {
	if _, err := NewAWSProvider(Config{Client: nil}); !errors.Is(err, ErrNoClient) {
		t.Fatalf("NewAWSProvider(nil client) err = %v, want ErrNoClient", err)
	}
}

// InstanceTypeFor is the directly-tested mapping helper (extra mutation guard).
func TestInstanceTypeFor(t *testing.T) {
	if got, ok := InstanceTypeFor("h100"); !ok || got != "p5.48xlarge" {
		t.Fatalf("InstanceTypeFor(h100) = %q,%v", got, ok)
	}
	if _, ok := InstanceTypeFor("tpu-v5"); ok {
		t.Fatalf("InstanceTypeFor(tpu-v5) returned ok=true for an unmapped model")
	}
}
