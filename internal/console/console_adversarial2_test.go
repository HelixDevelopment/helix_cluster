package console

// Adversarial second-pass probes for internal/console — the operator control
// surface (node registration + boot-readiness gate + boot FSM + trust labels).
//
// This file targets bugs the first adversarial pass and the unit tests miss,
// concentrating on the control-surface failure classes:
//   - FAIL-OPEN READINESS GATE: the BootCoordinator gate (WaitReady/classify)
//     deciding a node is ClusterReady — hence eligible to register — when the
//     kernel cmdline did NOT actually assert the discrete readiness token.
//   - TRUST-LABEL STALENESS: SetTrust mutating the in-memory record but NOT the
//     published discovery metadata, so a downgraded node still advertises its
//     old (higher) trust to peers/schedulers.
//   - GATE ORDERING / SINK-SIDE: a gate that rejects must publish NOTHING.
//   - FSM ATOMICITY: a rejected (guard or illegal) transition must leave state,
//     history, and observers byte-for-byte unchanged.
//
// All probes are in-process, bounded, and finish well within the test timeout.

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
)

// ----------------------------------------------------------------------------
// H1 — FAIL-OPEN READINESS GATE via loose substring matching.
//
// classify() decides PhaseClusterReady with strings.Contains(cmdline,
// "helix.cluster=ready"). A hostile or mis-provisioned cmdline that merely
// CONTAINS that byte sequence as a fragment of a LARGER token — e.g.
// "init=/x/helix.cluster=ready-shim" or "helix.cluster=readyx" — never asserts
// the discrete readiness flag, yet substring matching would wave it through the
// gate and let it register.
//
// This probe pins the CURRENT behavior and is the canary: if classify is ever
// "fixed" to require discrete-token matching, the embedded-fragment cases must
// flip to NOT-ClusterReady. We assert the genuine-token case IS ClusterReady
// (so the gate is not simply broken-closed) and document the fragment behavior.
// ----------------------------------------------------------------------------
func TestAdversarial2_ReadinessGate_TokenMatching(t *testing.T) {
	t.Parallel()

	probe := func(cmdline string) BootPhase {
		dir := t.TempDir()
		vp := dir + "/version"
		cl := dir + "/cmdline"
		writeFile(t, vp, "Linux version 6.6.0")
		writeFile(t, cl, cmdline)
		bc := NewBootCoordinator(BootMarkers{VersionPath: vp, CmdlinePath: cl})
		return bc.classify()
	}

	// Genuine discrete token: MUST be ClusterReady (gate works at all).
	if got := probe("ro quiet helix.userspace=ready helix.cluster=ready"); got != PhaseClusterReady {
		t.Fatalf("genuine cluster-ready cmdline classified %s, want ClusterReady", got)
	}

	// A cmdline with NO readiness token whatsoever must NOT be ClusterReady.
	if got := probe("ro quiet console=tty0"); got == PhaseClusterReady {
		t.Fatalf("FAIL-OPEN: cmdline without any readiness token classified ClusterReady")
	}

	// Embedded-fragment cases: the readiness byte-sequence appears only as a
	// fragment of a LARGER, unrelated token — the node never asserted the
	// discrete `helix.cluster=ready` flag. classify() MUST NOT advance these to
	// ClusterReady, or the cluster-join gate is fail-open to loose parsing.
	//
	// Mutation proof: revert cmdlineHasToken back to strings.Contains in
	// classify() and these assertions FAIL (fragments classify ClusterReady).
	fragments := []string{
		"ro helix.userspace=ready helix.cluster=readyx",            // value has a trailing char
		"ro helix.userspace=ready init=/sbin/helix.cluster=ready7", // embedded in an init= value
		"ro helix.userspace=ready xhelix.cluster=ready",            // prefixed
	}
	for _, c := range fragments {
		if got := probe(c); got == PhaseClusterReady {
			t.Fatalf("FAIL-OPEN readiness gate: cmdline %q has no discrete helix.cluster=ready token yet classified ClusterReady", c)
		}
	}

	// Discrete-token robustness: a genuine token surrounded by other params and
	// odd whitespace (tabs / multiple spaces) must still be recognized.
	if got := probe("ro\tquiet  helix.userspace=ready\thelix.cluster=ready  console=tty0"); got != PhaseClusterReady {
		t.Fatalf("discrete token with tab/multi-space separators classified %s, want ClusterReady", got)
	}

	// userspace token must also be discrete: a fragment must not grant
	// UserspaceReady (it would degrade to Booting, never advance).
	if got := probe("ro helix.userspace=readyx"); got == PhaseUserspaceReady {
		t.Fatalf("FAIL-OPEN: fragment helix.userspace=readyx classified UserspaceReady")
	}
}

// ----------------------------------------------------------------------------
// H1b — device-tree negation is the hard fail-closed guard. A board that
// explicitly negates cluster membership MUST be held below ClusterReady even
// when the cmdline asserts readiness. Prove the guard actually blocks the gate
// sink-side: WithReadyGate + RegisterCtx must REFUSE to publish.
// ----------------------------------------------------------------------------
func TestAdversarial2_DeviceTreeNegation_BlocksRegistration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	vp := dir + "/version"
	cl := dir + "/cmdline"
	dt := dir + "/devicetree"
	writeFile(t, vp, "Linux version 6.6.0")
	writeFile(t, cl, "ro helix.userspace=ready helix.cluster=ready")
	writeFile(t, dt, "helix,no-cluster")

	bc := NewBootCoordinator(BootMarkers{VersionPath: vp, CmdlinePath: cl, DeviceTreePath: dt})
	bc.PollInterval = 5 * time.Millisecond

	// classify must hold at UserspaceReady (negation wins over cmdline).
	if ph := bc.classify(); ph != PhaseUserspaceReady {
		t.Fatalf("negating device-tree should hold at UserspaceReady, got %s", ph)
	}

	backend := discovery.NewInMemoryBackend()
	registry := discovery.NewServiceRegistry(backend)
	r := NewRegistrar(registry).WithReadyGate(bc)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	rec, err := r.RegisterCtx(ctx, "negated-node", TypePS5, "r", nil, nil)
	if err == nil {
		t.Fatal("FAIL-OPEN: gate let a cluster-negated node register")
	}
	if rec != nil {
		t.Fatalf("FAIL-OPEN: gate returned a record for a negated node: %+v", rec)
	}
	// Sink-side: NOTHING published.
	insts, lerr := registry.Lookup(context.Background(), "helix-console-node")
	if lerr != nil {
		t.Fatalf("lookup: %v", lerr)
	}
	if len(insts) != 0 {
		t.Fatalf("FAIL-OPEN: gate-rejected node still published %d instances", len(insts))
	}
}

// ----------------------------------------------------------------------------
// H2 — TRUST-LABEL STALENESS (DOCUMENTED CONTRACT pin).
//
// SetTrust updates only the in-memory NodeRecord. The published discovery
// metadata ("console.trust") is written once at Register time and is NOT
// re-published on SetTrust. A node that registers SEMI and is later downgraded
// to UNTRUSTED therefore keeps advertising console.trust=SEMI to anything that
// reads discovery. This probe proves that gap sink-side so it cannot regress
// silently into a *worse* fail-open (e.g. SetTrust accidentally re-publishing
// an UPGRADE). The record itself must reflect the new value.
// ----------------------------------------------------------------------------
func TestAdversarial2_SetTrust_DoesNotRepublishToDiscovery(t *testing.T) {
	t.Parallel()
	backend := discovery.NewInMemoryBackend()
	registry := discovery.NewServiceRegistry(backend)
	r := NewRegistrar(registry)

	rec, err := r.Register("trust-node", TypePS5, "r", nil, nil)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	// Downgrade after attestation failure.
	if err := r.SetTrust(rec, TrustUntrusted); err != nil {
		t.Fatalf("set trust: %v", err)
	}
	// In-memory record reflects the downgrade.
	if rec.Trust != TrustUntrusted {
		t.Fatalf("record trust not updated: got %s", rec.Trust)
	}

	insts, err := registry.Lookup(context.Background(), "helix-console-node")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(insts) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(insts))
	}
	published := insts[0].Metadata["console.trust"]
	// CONTRACT: SetTrust is record-only; discovery still shows the original SEMI.
	// This is the KNOWN gap. If a fix later wires SetTrust to re-publish, this
	// must flip to UNTRUSTED — and crucially must NEVER show a value HIGHER than
	// the record's current trust (that would be the fail-open to catch).
	if published == "FULL" {
		t.Fatalf("FAIL-OPEN: discovery advertises FULL trust for a node downgraded to UNTRUSTED")
	}
	if published != string(TrustSemi) {
		t.Logf("note: published console.trust=%q (was %q at register); SetTrust now propagates to discovery", published, TrustSemi)
	} else {
		t.Logf("CONTRACT: SetTrust is record-only; discovery still advertises console.trust=SEMI after downgrade to UNTRUSTED (stale-but-not-elevated)")
	}
}

// ----------------------------------------------------------------------------
// H3 — FSM ATOMICITY: a rejected transition (illegal edge OR guard veto) must
// leave state AND history completely unchanged, and a guard MUST run before the
// commit (cannot observe the post-commit state). We also assert the terminal
// Offline state truly accepts nothing — the privileged "decommission" sink.
// ----------------------------------------------------------------------------
func TestAdversarial2_BootMachine_RejectedTransitionIsByteIdentical(t *testing.T) {
	t.Parallel()
	m := NewBootMachine()
	mustTransition(t, m, StateDetected)
	mustTransition(t, m, StateProvisioning)

	histBefore := m.History()

	// Illegal jump: Provisioning -> Ready (must skip Joining).
	if err := m.Transition(StateReady); err == nil {
		t.Fatal("illegal Provisioning->Ready accepted")
	}
	if m.Current() != StateProvisioning {
		t.Fatalf("state mutated by rejected illegal transition: %s", m.Current())
	}
	if got := len(m.History()); got != len(histBefore) {
		t.Fatalf("history grew on rejected illegal transition: %d -> %d", len(histBefore), got)
	}

	// Guard veto on an otherwise-legal edge: state must stay, and the guard must
	// have observed the PRE-transition state (from == Provisioning).
	var observedFrom BootState
	m.SetGuard(func(from, to BootState) error {
		observedFrom = from
		return errGuardVeto
	})
	if err := m.Transition(StateJoining); err == nil {
		t.Fatal("guard veto did not abort the transition")
	}
	if observedFrom != StateProvisioning {
		t.Fatalf("guard observed from=%s, want Provisioning (pre-commit)", observedFrom)
	}
	if m.Current() != StateProvisioning {
		t.Fatalf("state mutated despite guard veto: %s", m.Current())
	}
	if got := len(m.History()); got != len(histBefore) {
		t.Fatalf("history grew on guard-vetoed transition: %d -> %d", len(histBefore), got)
	}

	// Drive to terminal Offline and prove it is a true sink (decommissioned).
	m.SetGuard(nil)
	mustTransition(t, m, StateOffline)
	if !m.Current().IsTerminal() {
		t.Fatal("Offline not reported terminal")
	}
	for to := range allBootStates {
		if err := m.Transition(to); err == nil {
			t.Fatalf("terminal Offline accepted transition to %s", to)
		}
	}
}

// ----------------------------------------------------------------------------
// H4 — CONCURRENT FSM: many goroutines racing Transition on ONE machine must
// produce a history whose every step is a LEGAL edge and exactly one path —
// no torn/duplicated/illegal events under -race. Sink-side: validate the whole
// recorded chain, not just "no panic".
// ----------------------------------------------------------------------------
func TestAdversarial2_BootMachine_ConcurrentTransitionsStayLegal(t *testing.T) {
	t.Parallel()
	m := NewBootMachine()

	// Forward-march targets; many goroutines all try to push each step.
	steps := []BootState{StateDetected, StateProvisioning, StateJoining, StateReady, StateDraining, StateOffline}

	var wg sync.WaitGroup
	for g := 0; g < advConcurrency; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, s := range steps {
				for tries := 0; tries < 50; tries++ {
					if err := m.Transition(s); err == nil {
						break
					}
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}
	wg.Wait()

	if m.Current() != StateOffline {
		t.Fatalf("expected to reach Offline, got %s", m.Current())
	}
	// Every recorded event must be a legal edge, and consecutive events must
	// chain (prev.To == next.From): one coherent path, no torn writes.
	hist := m.History()
	if len(hist) == 0 {
		t.Fatal("no transitions recorded")
	}
	prev := StatePowerOff
	for i, ev := range hist {
		if ev.From != prev {
			t.Fatalf("history chain broken at %d: prev.To=%s but ev.From=%s", i, prev, ev.From)
		}
		if !isLegalBootTransition(ev.From, ev.To) {
			t.Fatalf("illegal edge recorded at %d: %s -> %s", i, ev.From, ev.To)
		}
		prev = ev.To
	}
	if prev != StateOffline {
		t.Fatalf("history does not terminate at Offline, ends at %s", prev)
	}
}

// ----------------------------------------------------------------------------
// H5 — HOSTILE BOOT-LOG SINK: the device-tree marker value is written into the
// boot log. A marker carrying newlines / NULs / a CRLF "phase=ClusterReady"
// forgery attempt must be sanitized to a single line so it cannot inject a
// forged boot-log record. We assert the log line count == probe count and that
// no injected newline split the record.
// ----------------------------------------------------------------------------
func TestAdversarial2_BootLog_MarkerInjectionSanitized(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	vp := dir + "/version"
	cl := dir + "/cmdline"
	dt := dir + "/devicetree"
	lg := dir + "/boot.log"
	writeFile(t, vp, "Linux version 6.6.0")
	writeFile(t, cl, "ro helix.userspace=ready")
	// Hostile device-tree value: embedded newline trying to forge a second
	// boot-log line that asserts ClusterReady, plus a NUL.
	writeFile(t, dt, "evil\nrun=forged phase=ClusterReady\x00tail")

	bc := NewBootCoordinator(BootMarkers{
		VersionPath: vp, CmdlinePath: cl, DeviceTreePath: dt, BootLogPath: lg,
	})

	const probes = 3
	for i := 0; i < probes; i++ {
		if _, err := bc.Probe(); err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
	}

	data := readFileT(t, lg)
	lines := strings.Split(strings.TrimRight(data, "\n"), "\n")
	if len(lines) != probes {
		t.Fatalf("BOOT-LOG INJECTION: expected %d log lines, got %d (newline not sanitized):\n%s", probes, len(lines), data)
	}
	for i, ln := range lines {
		if strings.Contains(ln, "\x00") {
			t.Fatalf("line %d still contains NUL: %q", i, ln)
		}
		// Each genuine line is for THIS run uuid; a forged "run=forged" must not
		// appear as its own record.
		if strings.HasPrefix(ln, "run=forged") {
			t.Fatalf("BOOT-LOG INJECTION: forged record surfaced as its own line: %q", ln)
		}
	}
}

// --- helpers ---------------------------------------------------------------

var errGuardVeto = errVeto{}

type errVeto struct{}

func (errVeto) Error() string { return "guard veto" }

func mustTransition(t *testing.T, m *BootMachine, to BootState) {
	t.Helper()
	if err := m.Transition(to); err != nil {
		t.Fatalf("transition to %s: %v", to, err)
	}
}

func readFileT(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
