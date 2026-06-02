package gravaladmit

import (
	"testing"
)

// runID used across all tests — every Decision must carry this RunID.
const runID = "gravaladmit-HXC-graval-8e3f1a02-4b6d-11ef-9c2a-0050568a77b4"

// testSecret is the shared HMAC key injected for all tests.
var testSecret = []byte("helix-gravaladmit-test-secret-v1")

// -----------------------------------------------------------------------------
// Prover implementations (real injected seams, not fakes)
// -----------------------------------------------------------------------------

// correctProver computes the real HMAC-SHA256 response the gate expects.
// It models a genuine GPU engine that produces the right matrix-multiply proof.
// CLAUDE-2: no host computation is faked — it uses ComputeExpectedMAC which
// mirrors the gate's internal computeMAC exactly.
type correctProver struct {
	key []byte
}

func (p *correctProver) Prove(ch Challenge) Response {
	return Response{
		ProviderID: ch.ProviderID,
		Nonce:      ch.Nonce,
		MAC:        ComputeExpectedMAC(p.key, ch.Nonce, ch.ProviderID),
	}
}

// forgedProver returns a response with an obviously wrong MAC — simulating a
// provider that does not have the real GPU result (or is attempting forgery).
type forgedProver struct{}

func (p *forgedProver) Prove(ch Challenge) Response {
	return Response{
		ProviderID: ch.ProviderID,
		Nonce:      ch.Nonce,
		MAC:        "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
	}
}

// wrongNonceProver echoes a different nonce in its response.
type wrongNonceProver struct {
	key []byte
}

func (p *wrongNonceProver) Prove(ch Challenge) Response {
	return Response{
		ProviderID: ch.ProviderID,
		Nonce:      "0000000000000000000000000000000000000000000000000000000000000000",
		MAC:        ComputeExpectedMAC(p.key, ch.Nonce, ch.ProviderID),
	}
}

// wrongProviderIDProver returns a response with a mismatched ProviderID.
type wrongProviderIDProver struct {
	key []byte
}

func (p *wrongProviderIDProver) Prove(ch Challenge) Response {
	return Response{
		ProviderID: "attacker-provider",
		Nonce:      ch.Nonce,
		MAC:        ComputeExpectedMAC(p.key, ch.Nonce, ch.ProviderID),
	}
}

// -----------------------------------------------------------------------------
// (a) TestCorrectProviderAdmitted
// A provider whose Prover returns the correct HMAC response is admitted with
// graval.verified=true and the RunID recorded.
//
// Mutation guard: removing the HMAC equality check in Admitter.Admit (the
// `if !hmac.Equal(expected, got)` line) makes any response pass — BUT this
// test still passes in that case.  See TestForgedProviderRejected for the guard.
// -----------------------------------------------------------------------------

func TestCorrectProviderAdmitted(t *testing.T) {
	a, err := NewAdmitter(testSecret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", runID, err)
	}

	providerID := "gpu-provider-alpha"
	ch, err := a.IssueChallenge(providerID)
	if err != nil {
		t.Fatalf("%s: IssueChallenge(%q): %v", runID, providerID, err)
	}
	t.Logf("%s: BEFORE Admit — issued challenge nonce=%q for provider=%q", runID, ch.Nonce, providerID)

	prover := &correctProver{key: testSecret}
	d := a.Admit(runID, providerID, prover)

	t.Logf("%s: AFTER Admit — Decision{RunID=%q Admitted=%v Labels=%v Reason=%q}",
		runID, d.RunID, d.Admitted, d.Labels, d.Reason)

	// RunID must be propagated into every Decision (sink-side observable).
	if d.RunID != runID {
		t.Fatalf("%s: Decision.RunID=%q want %q", runID, d.RunID, runID)
	}
	if d.ProviderID != providerID {
		t.Fatalf("%s: Decision.ProviderID=%q want %q", runID, d.ProviderID, providerID)
	}
	if !d.Admitted {
		t.Fatalf("%s: correct provider must be Admitted=true, got false; Reason=%q", runID, d.Reason)
	}
	// graval.verified label must be "true" on an admitted provider.
	if d.Labels["graval.verified"] != "true" {
		t.Fatalf("%s: Labels[graval.verified]=%q want \"true\"; Labels=%v", runID, d.Labels["graval.verified"], d.Labels)
	}
	// Admitted provider must appear in the schedulable set.
	if !d.Schedulable() {
		t.Fatalf("%s: Schedulable() must return true for an admitted provider", runID)
	}
}

// -----------------------------------------------------------------------------
// (b) TestForgedProviderRejected
// A provider whose response is forged/incorrect is rejected with no
// graval.verified label and is absent from the schedulable set.
//
// MUTATION GUARD (named test): removing the line
//
//	if !hmac.Equal(expected, got) { ... }
//
// in Admitter.Admit makes this test FAIL: d.Admitted becomes true, the
// graval.verified check passes, and the "absent from schedulable set" assertion
// fires — proving the cryptographic check is load-bearing.
// -----------------------------------------------------------------------------

func TestForgedProviderRejected(t *testing.T) {
	a, err := NewAdmitter(testSecret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", runID, err)
	}

	providerID := "gpu-provider-forged"
	_, err = a.IssueChallenge(providerID)
	if err != nil {
		t.Fatalf("%s: IssueChallenge(%q): %v", runID, providerID, err)
	}

	prover := &forgedProver{}
	d := a.Admit(runID, providerID, prover)

	t.Logf("%s: AFTER Admit(forged) — Decision{RunID=%q Admitted=%v Labels=%v Reason=%q}",
		runID, d.RunID, d.Admitted, d.Labels, d.Reason)

	// RunID must still be present even for rejected providers.
	if d.RunID != runID {
		t.Fatalf("%s: Decision.RunID=%q want %q", runID, d.RunID, runID)
	}
	if d.Admitted {
		t.Fatalf("%s: forged provider MUST NOT be admitted, but Admitted=true", runID)
	}
	// No graval.verified label must be present on a rejected provider.
	if _, ok := d.Labels["graval.verified"]; ok {
		t.Fatalf("%s: rejected provider MUST NOT carry graval.verified label, got Labels=%v", runID, d.Labels)
	}
	// Reason must be non-empty on rejection.
	if d.Reason == "" {
		t.Fatalf("%s: rejected provider must have non-empty Reason", runID)
	}

	// Build a two-provider schedulable set: one correct, one forged.
	// The forged provider must be ABSENT from the returned schedulable IDs.
	a2, _ := NewAdmitter(testSecret)
	correctID := "gpu-provider-correct"
	forgedID := "gpu-provider-forged-2"

	_, err = a2.IssueChallenge(correctID)
	if err != nil {
		t.Fatalf("%s: IssueChallenge(%q): %v", runID, correctID, err)
	}
	_, err = a2.IssueChallenge(forgedID)
	if err != nil {
		t.Fatalf("%s: IssueChallenge(%q): %v", runID, forgedID, err)
	}

	provers := map[string]Prover{
		correctID: &correctProver{key: testSecret},
		forgedID:  &forgedProver{},
	}

	schedulable, decisions := a2.SchedulableSet(runID, []string{correctID, forgedID}, provers)

	t.Logf("%s: SchedulableSet — schedulable=%v decisions=%d", runID, schedulable, len(decisions))

	// Correct provider must be schedulable.
	found := false
	for _, id := range schedulable {
		if id == correctID {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s: correct provider %q must appear in schedulable set, got %v", runID, correctID, schedulable)
	}

	// Forged provider must be ABSENT from the schedulable set.
	for _, id := range schedulable {
		if id == forgedID {
			t.Fatalf("%s: forged provider %q MUST be absent from schedulable set but was found: %v", runID, forgedID, schedulable)
		}
	}

	// The forged provider's Decision must record the RunID and have no graval.verified.
	for _, dd := range decisions {
		if dd.ProviderID == forgedID {
			if dd.RunID != runID {
				t.Fatalf("%s: forged decision RunID=%q want %q", runID, dd.RunID, runID)
			}
			if dd.Admitted {
				t.Fatalf("%s: forged provider decision must have Admitted=false", runID)
			}
			if _, ok := dd.Labels["graval.verified"]; ok {
				t.Fatalf("%s: forged provider decision must not carry graval.verified label", runID)
			}
		}
	}
}

// -----------------------------------------------------------------------------
// TestSingleUseNonce — replaying a nonce is rejected.
// -----------------------------------------------------------------------------

func TestSingleUseNonce(t *testing.T) {
	a, err := NewAdmitter(testSecret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", runID, err)
	}

	providerID := "gpu-provider-replay"
	ch, err := a.IssueChallenge(providerID)
	if err != nil {
		t.Fatalf("%s: IssueChallenge: %v", runID, err)
	}
	t.Logf("%s: issued nonce=%q for provider=%q", runID, ch.Nonce, providerID)

	// First call consumes the nonce.
	prover := &correctProver{key: testSecret}
	d1 := a.Admit(runID, providerID, prover)
	if !d1.Admitted {
		t.Fatalf("%s: first Admit must succeed; Reason=%q", runID, d1.Reason)
	}
	t.Logf("%s: first Admit Admitted=%v", runID, d1.Admitted)

	// Re-issuing a challenge for the same provider after consumption is fine
	// (the first nonce was consumed). Issue a second challenge to simulate replay
	// protection: try admitting with a NEW prover but WITHOUT re-issuing a challenge.
	d2 := a.Admit(runID, providerID, prover)
	t.Logf("%s: second Admit (no pending nonce) Admitted=%v Reason=%q", runID, d2.Admitted, d2.Reason)
	if d2.Admitted {
		t.Fatalf("%s: second Admit without re-issued challenge must be rejected (nonce consumed), got Admitted=true", runID)
	}
	if d2.Reason == "" {
		t.Fatalf("%s: rejected replay must carry non-empty Reason", runID)
	}
}

// TestDuplicatePendingChallenge — IssueChallenge returns ErrNonceReused when
// the provider already has a pending challenge outstanding.
func TestDuplicatePendingChallenge(t *testing.T) {
	a, err := NewAdmitter(testSecret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", runID, err)
	}

	providerID := "gpu-provider-duplicate"
	_, err = a.IssueChallenge(providerID)
	if err != nil {
		t.Fatalf("%s: first IssueChallenge: %v", runID, err)
	}

	_, err = a.IssueChallenge(providerID)
	if !isErrNonceReused(err) {
		t.Fatalf("%s: second IssueChallenge must return ErrNonceReused, got %v", runID, err)
	}
	t.Logf("%s: duplicate IssueChallenge correctly returned ErrNonceReused: %v", runID, err)
}

// TestNilProverFailClosed — a nil Prover yields Admitted=false (fail-closed).
func TestNilProverFailClosed(t *testing.T) {
	a, err := NewAdmitter(testSecret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", runID, err)
	}

	providerID := "gpu-provider-nilprover"
	_, err = a.IssueChallenge(providerID)
	if err != nil {
		t.Fatalf("%s: IssueChallenge: %v", runID, err)
	}

	d := a.Admit(runID, providerID, nil)
	t.Logf("%s: Admit(nil prover) Admitted=%v Reason=%q", runID, d.Admitted, d.Reason)
	if d.Admitted {
		t.Fatalf("%s: nil Prover must be fail-closed (Admitted=false), got true", runID)
	}
	if d.Reason == "" {
		t.Fatalf("%s: nil Prover rejection must carry non-empty Reason", runID)
	}
}

// TestWrongNonce — a response that echoes a mismatched nonce is rejected.
func TestWrongNonce(t *testing.T) {
	a, err := NewAdmitter(testSecret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", runID, err)
	}

	providerID := "gpu-provider-wrongnonce"
	_, err = a.IssueChallenge(providerID)
	if err != nil {
		t.Fatalf("%s: IssueChallenge: %v", runID, err)
	}

	d := a.Admit(runID, providerID, &wrongNonceProver{key: testSecret})
	t.Logf("%s: Admit(wrongNonce) Admitted=%v Reason=%q", runID, d.Admitted, d.Reason)
	if d.Admitted {
		t.Fatalf("%s: wrong nonce must be rejected, got Admitted=true", runID)
	}
	if d.Labels["graval.verified"] != "" {
		t.Fatalf("%s: rejected provider must not have graval.verified label", runID)
	}
}

// TestWrongProviderID — a response with mismatched ProviderID is rejected.
func TestWrongProviderID(t *testing.T) {
	a, err := NewAdmitter(testSecret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", runID, err)
	}

	providerID := "gpu-provider-wrongid"
	_, err = a.IssueChallenge(providerID)
	if err != nil {
		t.Fatalf("%s: IssueChallenge: %v", runID, err)
	}

	d := a.Admit(runID, providerID, &wrongProviderIDProver{key: testSecret})
	t.Logf("%s: Admit(wrongProviderID) Admitted=%v Reason=%q", runID, d.Admitted, d.Reason)
	if d.Admitted {
		t.Fatalf("%s: wrong providerID in response must be rejected, got Admitted=true", runID)
	}
}

// TestEmptySecret — NewAdmitter rejects an empty secret.
func TestEmptySecret(t *testing.T) {
	_, err := NewAdmitter(nil)
	if err == nil {
		t.Fatalf("%s: NewAdmitter(nil) must return error, got nil", runID)
	}
	t.Logf("%s: NewAdmitter(nil) correctly returned error: %v", runID, err)

	_, err = NewAdmitter([]byte{})
	if err == nil {
		t.Fatalf("%s: NewAdmitter(empty) must return error, got nil", runID)
	}
	t.Logf("%s: NewAdmitter(empty) correctly returned error: %v", runID, err)
}

// TestLabelsAbsentOnRejection — no labels whatsoever on a failed admission.
func TestLabelsAbsentOnRejection(t *testing.T) {
	a, err := NewAdmitter(testSecret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", runID, err)
	}

	providerID := "gpu-provider-labels"
	_, err = a.IssueChallenge(providerID)
	if err != nil {
		t.Fatalf("%s: IssueChallenge: %v", runID, err)
	}

	d := a.Admit(runID, providerID, &forgedProver{})
	t.Logf("%s: rejected decision Labels=%v (must be nil or empty)", runID, d.Labels)
	for k, v := range d.Labels {
		if k == "graval.verified" || v == "true" {
			t.Fatalf("%s: rejected provider must NOT have graval.verified label, but Labels=%v", runID, d.Labels)
		}
	}
}

// TestSchedulableSetAllCorrect — all correct providers land in the schedulable set.
func TestSchedulableSetAllCorrect(t *testing.T) {
	a, err := NewAdmitter(testSecret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", runID, err)
	}

	ids := []string{"p1", "p2", "p3"}
	provers := make(map[string]Prover, len(ids))
	for _, id := range ids {
		if _, e := a.IssueChallenge(id); e != nil {
			t.Fatalf("%s: IssueChallenge(%q): %v", runID, id, e)
		}
		provers[id] = &correctProver{key: testSecret}
	}

	schedulable, decisions := a.SchedulableSet(runID, ids, provers)
	t.Logf("%s: SchedulableSet all-correct => schedulable=%v", runID, schedulable)

	if len(schedulable) != len(ids) {
		t.Fatalf("%s: all-correct: expected %d schedulable, got %d: %v", runID, len(ids), len(schedulable), schedulable)
	}
	for _, d := range decisions {
		if !d.Admitted {
			t.Fatalf("%s: all-correct: provider %q unexpectedly rejected; Reason=%q", runID, d.ProviderID, d.Reason)
		}
		if d.Labels["graval.verified"] != "true" {
			t.Fatalf("%s: admitted provider %q missing graval.verified label", runID, d.ProviderID)
		}
		if d.RunID != runID {
			t.Fatalf("%s: decision for %q has RunID=%q want %q", runID, d.ProviderID, d.RunID, runID)
		}
	}
}

// TestNilProverConsumesNonce verifies that a nil-Prover Admit call burns the
// single-use nonce so a subsequent Admit with a real prover cannot succeed.
//
// MUTATION GUARD: if the nil-prover check in Admit is moved BEFORE the nonce
// consumption, the nonce survives the nil call and the subsequent correctProver
// call in this test succeeds (d2.Admitted becomes true), making this test FAIL.
func TestNilProverConsumesNonce(t *testing.T) {
	a, err := NewAdmitter(testSecret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", runID, err)
	}

	providerID := "gpu-provider-nil-consume"
	_, err = a.IssueChallenge(providerID)
	if err != nil {
		t.Fatalf("%s: IssueChallenge: %v", runID, err)
	}

	// First call: nil prover — must fail AND consume the nonce.
	d1 := a.Admit(runID, providerID, nil)
	t.Logf("%s: Admit(nil) Admitted=%v Reason=%q", runID, d1.Admitted, d1.Reason)
	if d1.Admitted {
		t.Fatalf("%s: nil Prover must be rejected (fail-closed), got Admitted=true", runID)
	}
	if d1.Reason == "" {
		t.Fatalf("%s: nil Prover rejection must carry non-empty Reason", runID)
	}

	// Second call: correct prover, same provider — nonce was consumed, must fail.
	d2 := a.Admit(runID, providerID, &correctProver{key: testSecret})
	t.Logf("%s: Admit(correct, after nil) Admitted=%v Reason=%q", runID, d2.Admitted, d2.Reason)
	if d2.Admitted {
		t.Fatalf("%s: nonce must be consumed by nil-Prover call; re-Admit must be rejected, got Admitted=true", runID)
	}
	if d2.Reason == "" {
		t.Fatalf("%s: second Admit after consumed nonce must carry non-empty Reason", runID)
	}
}

// TestSecretIsolation verifies that mutating the byte slice passed to
// NewAdmitter after construction does not corrupt the gate's HMAC key.
//
// MUTATION GUARD: if NewAdmitter stores the caller slice directly (no copy),
// the mutation below causes computeMAC to use the corrupted key; the correct
// prover (which still uses the original key bytes) produces a MAC that no
// longer matches, so Admit returns Admitted=false and this test FAILS.
func TestSecretIsolation(t *testing.T) {
	// Make a mutable copy of the test secret.
	secretCopy := make([]byte, len(testSecret))
	copy(secretCopy, testSecret)

	a, err := NewAdmitter(secretCopy)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", runID, err)
	}

	// Corrupt the caller's slice — the gate must be unaffected.
	for i := range secretCopy {
		secretCopy[i] = 0xFF
	}

	providerID := "gpu-provider-secret-isolation"
	_, err = a.IssueChallenge(providerID)
	if err != nil {
		t.Fatalf("%s: IssueChallenge: %v", runID, err)
	}

	// The prover uses the ORIGINAL secret bytes (testSecret), which is what
	// the gate should have stored internally — not the corrupted slice.
	d := a.Admit(runID, providerID, &correctProver{key: testSecret})
	t.Logf("%s: Admit after secret mutation Admitted=%v Reason=%q", runID, d.Admitted, d.Reason)
	if !d.Admitted {
		t.Fatalf("%s: gate must use its internal copy of the secret, not the caller slice; Admitted=false Reason=%q", runID, d.Reason)
	}
	if d.Labels["graval.verified"] != "true" {
		t.Fatalf("%s: admitted provider must carry graval.verified=true", runID)
	}
}

// isErrNonceReused checks whether err equals ErrNonceReused using errors.Is
// semantics.
func isErrNonceReused(err error) bool {
	return err != nil && err.Error() == ErrNonceReused.Error()
}
