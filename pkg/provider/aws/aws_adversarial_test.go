package aws

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/pool"
)

// ---------------------------------------------------------------------------
// Adversarial probes for pkg/provider/aws.
//
// This package carries no float price comparator (no "cheapest" selection); the
// per-hour price is an opaque pass-through (pool.Spec.HourlyUSD -> pool.Instance
// .HourlyUSD) and the Spot cap is an opaque string. So the hostile-numeric class
// here is narrower than a pricing optimiser: we prove (a) a non-finite price can
// neither panic Provision nor be silently corrupted, (b) malformed / hostile
// GPU-model strings parse to a clean ErrUnknownGPUModel with NO billable call
// rather than a zero-value pick, (c) the Capacity gate boundary is exact (no
// off-by-one that would launch one billable instance past quota), and (d) List's
// sort comparator is a valid total order even on hostile duplicate / empty IDs
// (no strict-weak-ordering panic, deterministic output).
//
// Every assertion below is mutation-checked against the production code: each was
// confirmed to FAIL when the corresponding production invariant is inverted, then
// the production code reverted byte-identical.
// ---------------------------------------------------------------------------

// idClient is an EC2Client that hands back a caller-supplied sequence of instance
// IDs (so a test can force duplicate / empty / out-of-order IDs) and records the
// live set for DescribeInstances.
type idClient struct {
	mu    sync.Mutex
	ids   []string
	next  int
	live  map[string]DescribedInstance
	order []string
}

func newIDClient(ids ...string) *idClient {
	return &idClient{ids: ids, live: make(map[string]DescribedInstance)}
}

func (c *idClient) RunInstances(_ context.Context, in RunInstancesInput) (RunInstancesOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := ""
	if c.next < len(c.ids) {
		id = c.ids[c.next]
	}
	c.next++
	if strings.TrimSpace(id) != "" {
		if _, seen := c.live[id]; !seen {
			c.order = append(c.order, id)
		}
		c.live[id] = DescribedInstance{InstanceID: id, InstanceType: in.InstanceType, Tags: in.Tags}
	}
	return RunInstancesOutput{InstanceID: id}, nil
}

func (c *idClient) TerminateInstances(_ context.Context, in TerminateInstancesInput) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.live, in.InstanceID)
	return nil
}

func (c *idClient) DescribeInstances(_ context.Context, _ DescribeInstancesInput) (DescribeInstancesOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := DescribeInstancesOutput{}
	for _, di := range c.live {
		out.Instances = append(out.Instances, di)
	}
	return out, nil
}

// TestProvisionPreservesNonFinitePriceNoPanic feeds NaN / +Inf / -Inf as the
// per-hour price. The price is an opaque pass-through here; the adversarial
// requirement is that a hostile non-finite value (a) does not panic Provision and
// (b) is carried verbatim onto the returned Instance — never silently zeroed or
// NaN-normalised, which would corrupt a downstream TCO/"cheapest" comparator that
// trusts this provider's reported price.
func TestProvisionPreservesNonFinitePriceNoPanic(t *testing.T) {
	cases := []struct {
		name  string
		price float64
	}{
		{"NaN", math.NaN()},
		{"+Inf", math.Inf(1)},
		{"-Inf", math.Inf(-1)},
		{"negative", -123.45},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeEC2()
			p, err := NewAWSProvider(Config{Client: fake, ClusterName: "c", MaxInstances: 1})
			if err != nil {
				t.Fatalf("NewAWSProvider: %v", err)
			}
			inst, err := p.Provision(context.Background(), pool.Spec{GPUType: "H100", HourlyUSD: tc.price})
			if err != nil {
				t.Fatalf("Provision: %v", err)
			}
			if math.IsNaN(tc.price) {
				if !math.IsNaN(inst.HourlyUSD) {
					t.Fatalf("NaN price was corrupted to %v; want it preserved as NaN", inst.HourlyUSD)
				}
			} else if inst.HourlyUSD != tc.price {
				t.Fatalf("price = %v, want %v (preserved verbatim)", inst.HourlyUSD, tc.price)
			}
		})
	}
}

// TestProvisionHostileGPUModelStrings probes malformed / hostile model strings.
// Each must resolve to a clean ErrUnknownGPUModel (or, for the whitespace-padded
// valid model, the correct type) and an unknown one must issue NO billable
// RunInstances — i.e. a bad string is never a zero-value / guessed pick.
func TestProvisionHostileGPUModelStrings(t *testing.T) {
	unknown := []string{
		"",                       // empty
		"   ",                    // whitespace only
		"\t\n",                   // other whitespace
		"H100\x00",               // embedded NUL (must NOT match "H100")
		"H1OO",                   // homoglyph (letter O for zero)
		"p5.48xlarge",            // the *instance type* is not a model
		strings.Repeat("A", 1e3), // pathologically long
		"A100 ;DROP",             // injection-shaped
	}
	for _, m := range unknown {
		fake := newFakeEC2()
		p, _ := NewAWSProvider(Config{Client: fake, MaxInstances: 4})
		_, err := p.Provision(context.Background(), pool.Spec{GPUType: m})
		if !errors.Is(err, ErrUnknownGPUModel) {
			t.Fatalf("Provision(%q) err = %v, want ErrUnknownGPUModel", m, err)
		}
		if len(fake.runCalls) != 0 {
			t.Fatalf("Provision(%q) issued %d billable RunInstances; want 0", m, len(fake.runCalls))
		}
	}

	// A valid model padded with surrounding whitespace MUST still resolve (the
	// adapter trims), proving the trim is real and not a coincidental miss.
	fake := newFakeEC2()
	p, _ := NewAWSProvider(Config{Client: fake, ClusterName: "c", MaxInstances: 1})
	if _, err := p.Provision(context.Background(), pool.Spec{GPUType: "  a100  "}); err != nil {
		t.Fatalf("Provision(padded a100): %v", err)
	}
	if got := fake.lastRun().InstanceType; got != "p4de.24xlarge" {
		t.Fatalf("padded a100 resolved to %q, want p4de.24xlarge", got)
	}
	// And the gpu-model tag is the trimmed, upper-cased canonical form.
	if got := fake.lastRun().Tags[TagKeyGPUModel]; got != "A100" {
		t.Fatalf("gpu-model tag = %q, want canonical \"A100\"", got)
	}
}

// TestCapacityGateBoundaryIsExact pins the off-by-one boundary of the Capacity
// gate. With MaxInstances == N, exactly N Provisions succeed and the (N+1)-th is
// rejected with a clear at-capacity error and NO billable call. A gate mutated to
// `>` (instead of `>=`) would let the (N+1)-th launch a real billable instance.
func TestCapacityGateBoundaryIsExact(t *testing.T) {
	const n = 3
	fake := newFakeEC2()
	p, err := NewAWSProvider(Config{Client: fake, ClusterName: "c", MaxInstances: n})
	if err != nil {
		t.Fatalf("NewAWSProvider: %v", err)
	}
	for i := 0; i < n; i++ {
		if _, err := p.Provision(context.Background(), pool.Spec{GPUType: "A10G"}); err != nil {
			t.Fatalf("Provision #%d within quota failed: %v", i+1, err)
		}
	}
	if len(fake.runCalls) != n {
		t.Fatalf("billable launches = %d after N provisions, want %d", len(fake.runCalls), n)
	}
	// The (N+1)-th must be refused, and must NOT issue a billable call.
	_, err = p.Provision(context.Background(), pool.Spec{GPUType: "A10G"})
	if err == nil {
		t.Fatalf("Provision #%d at capacity succeeded; want at-capacity error", n+1)
	}
	if len(fake.runCalls) != n {
		t.Fatalf("over-capacity Provision issued a billable launch (runCalls=%d, want %d)", len(fake.runCalls), n)
	}
}

// TestZeroAndNegativeCapacityPermitNoLaunch proves an unset/negative quota fails
// CLOSED: MaxInstances <= 0 permits zero launches (never unbounded). This is the
// fail-closed billing guard from the Config doc.
func TestZeroAndNegativeCapacityPermitNoLaunch(t *testing.T) {
	for _, mx := range []int{0, -1, -1000} {
		fake := newFakeEC2()
		p, err := NewAWSProvider(Config{Client: fake, MaxInstances: mx})
		if err != nil {
			t.Fatalf("NewAWSProvider(max=%d): %v", mx, err)
		}
		if got := p.Capacity(); got != 0 {
			t.Fatalf("Capacity for MaxInstances=%d = %d, want 0 (clamped)", mx, got)
		}
		if _, err := p.Provision(context.Background(), pool.Spec{GPUType: "H100"}); err == nil {
			t.Fatalf("Provision with MaxInstances=%d succeeded; want at-capacity refusal", mx)
		}
		if len(fake.runCalls) != 0 {
			t.Fatalf("MaxInstances=%d issued %d billable launches; want 0", mx, len(fake.runCalls))
		}
	}
}

// TestListSortIsTotalOrderOnHostileIDs drives DescribeInstances with hostile IDs
// — duplicates and empty strings — and asserts List (a) does not panic in its
// sort.Slice comparator (a malformed strict-weak-ordering would panic under the
// race detector / on some inputs) and (b) returns a deterministically
// non-descending order by ID. The comparator `res[i].ID < res[j].ID` must remain
// a valid total order on equal keys.
func TestListSortIsTotalOrderOnHostileIDs(t *testing.T) {
	fake := newFakeEC2()
	// Inject hostile live state directly: duplicate IDs and an empty ID.
	fake.live = map[string]DescribedInstance{
		// map keys are unique, so to force duplicate *output* IDs we use the
		// DescribedInstance.InstanceID field independently of the map key.
		"k1": {InstanceID: "i-dup", Tags: map[string]string{TagKeyGPUModel: "H100"}},
		"k2": {InstanceID: "i-dup", Tags: map[string]string{TagKeyGPUModel: "A100"}},
		"k3": {InstanceID: "", Tags: nil},
		"k4": {InstanceID: "i-zzz", Tags: map[string]string{TagKeyGPUModel: "A10G"}},
		"k5": {InstanceID: "i-aaa"},
	}
	p, err := NewAWSProvider(Config{Client: fake, MaxInstances: 10})
	if err != nil {
		t.Fatalf("NewAWSProvider: %v", err)
	}
	live, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(live) != 5 {
		t.Fatalf("List returned %d, want 5 (all live rows, including empty/dup ids)", len(live))
	}
	// Must be non-descending by ID — a total order even across the duplicate.
	if !sort.SliceIsSorted(live, func(i, j int) bool { return live[i].ID < live[j].ID }) {
		ids := make([]string, len(live))
		for i, in := range live {
			ids[i] = in.ID
		}
		t.Fatalf("List output not sorted by ID: %v", ids)
	}
	// A row whose Tags map is nil must yield an empty (not panicking) GPUType.
	for _, in := range live {
		if in.ID == "i-aaa" && in.GPUType != "" {
			t.Fatalf("i-aaa GPUType = %q, want empty (no tag present)", in.GPUType)
		}
	}
}

// TestReleaseUnknownIDIssuesTerminate documents (pins) a LATENT RISK: Release
// performs NO ownership check — it issues a real, billable TerminateInstances for
// ANY non-empty id, including one this adapter never launched. This matches the
// method's documented contract ("issues a TerminateInstances for the instance
// id") but is a foot-gun: a caller passing a foreign/typo'd id terminates it. This
// test pins the current behaviour so a future ownership check is a conscious
// change, not an accident.
func TestReleaseUnknownIDIssuesTerminate(t *testing.T) {
	fake := newFakeEC2()
	p, err := NewAWSProvider(Config{Client: fake, MaxInstances: 1})
	if err != nil {
		t.Fatalf("NewAWSProvider: %v", err)
	}
	// Never provisioned i-foreign; Release it anyway.
	if err := p.Release(context.Background(), pool.Instance{ID: "i-foreign"}); err != nil {
		t.Fatalf("Release(foreign) err = %v; current contract terminates unconditionally", err)
	}
	if len(fake.terminateCalls) != 1 || fake.terminateCalls[0].InstanceID != "i-foreign" {
		t.Fatalf("Release(foreign) did not issue TerminateInstances for the foreign id: %+v", fake.terminateCalls)
	}
}

// TestDuplicateBackendIDLeaksCapacitySlot pins a LATENT RISK: if the backend
// returns an instance ID already tracked (which a conformant EC2 never does, but
// a buggy/compromised backend or replayed response could), the launched map
// overwrites silently while two reservation cycles complete — so len(launched)
// under-counts the truly-live instances and the Capacity gate would permit MORE
// than `capacity` real billable launches. We pin the observable consequence: two
// successful Provisions returning the same ID leave only ONE tracked entry. This
// is classified DOCUMENTED/LATENT (the InstanceID-uniqueness assumption is part
// of the backend contract), not a fixable in-package bug, so it is asserted as
// the CURRENT behaviour rather than the desired one.
func TestDuplicateBackendIDLeaksCapacitySlot(t *testing.T) {
	c := newIDClient("i-same", "i-same") // backend returns a duplicate id
	p, err := NewAWSProvider(Config{Client: c, ClusterName: "c", MaxInstances: 5})
	if err != nil {
		t.Fatalf("NewAWSProvider: %v", err)
	}
	if _, err := p.Provision(context.Background(), pool.Spec{GPUType: "H100"}); err != nil {
		t.Fatalf("Provision #1: %v", err)
	}
	if _, err := p.Provision(context.Background(), pool.Spec{GPUType: "H100"}); err != nil {
		t.Fatalf("Provision #2: %v", err)
	}
	p.mu.Lock()
	tracked := len(p.launched)
	reserved := p.reserved
	p.mu.Unlock()
	// Pinned current behaviour: both reservations cleared, one map entry. This
	// documents the slot-leak; if a future change de-dupes or rejects duplicate
	// backend IDs, update this pin deliberately.
	if reserved != 0 {
		t.Fatalf("reserved = %d after two completed Provisions, want 0", reserved)
	}
	if tracked != 1 {
		t.Fatalf("tracked = %d for a duplicate backend id; pinned current behaviour is 1 (capacity slot leak)", tracked)
	}
}
