package anticheat

// Adversarial sink-side probes for the anticheat HMAC verification boundary.
//
// anticheat is a trust boundary: ValidateToken returns the VERDICT
// (valid/honest vs invalid/cheating). The highest-severity class is a
// FAIL-OPEN: ValidateToken returning (true, ...) on empty / zero-value /
// malformed / cross-key / truncated evidence when it MUST reject.
//
// These tests assert the ACTUAL verdict (the bool/err pair), never merely
// "no panic". They are written to PASS against current prod (which is
// correctly fail-closed) and to BITE if prod regresses toward fail-open.
//
// Scope notes on the threat model:
//   - There is no floating-point score or `< threshold` guard anywhere in this
//     package, so the NUMERIC-POISON (NaN/Inf) and THRESHOLD-BOUNDARY axes have
//     no applicable sink here. The analogous "boundary" sink is the digest
//     LENGTH guard (len(supplied) != digestLen) and the constant-time compare;
//     both are probed below (truncated / over-length / all-zero tokens).
//   - The TOTAL-ORDER / IDENTITY axis maps to determinism + concurrency safety
//     of the stateless MAC: identical inputs must yield identical tokens with no
//     unguarded shared state under -race.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
)

// adv is a small run tag for forensic logs.
func adv(t *testing.T) string { t.Helper(); return newRunUUID(t) }

// ---------------------------------------------------------------------------
// (a) FAIL-OPEN: empty / zero-value evidence must NOT validate against the
//     wrong claim. The verdict (the bool) is asserted on the sink side.
// ---------------------------------------------------------------------------

// A token bound to EMPTY completion AND EMPTY revision must validate ONLY for
// that exact ("","") pair, and must REJECT any non-empty completion claim.
// A fail-open implementation that treated empty evidence as "trusted" (e.g.
// returned true whenever completion=="" ) would let a cheater submit an empty
// result and pass. Sink asserted: forged non-empty completion against the
// empty-bound token -> verdict MUST be false.
func TestAdversarial_EmptyEvidenceIsNotFailOpen(t *testing.T) {
	run := adv(t)
	secret := []byte("anti-cheat-key")

	// Genuine empty/empty token.
	tokEmpty := GenerateToken("", "", secret)
	if tokEmpty == "" {
		t.Fatalf("run=%s empty-evidence token unexpectedly empty (secret was non-empty)", run)
	}
	// The exact empty pair validates (baseline; empty is a legitimate value here).
	if ok, err := ValidateToken("", "", tokEmpty, secret); err != nil || !ok {
		t.Fatalf("run=%s baseline empty/empty did not validate ok=%v err=%v", run, ok, err)
	}

	// SINK: a cheater swapping in a real completion against the empty-bound token
	// must be REJECTED. If this returns true, completion is not bound -> fail-open.
	if ok, err := ValidateToken("real cheating output", "", tokEmpty, secret); err != nil || ok {
		t.Fatalf("run=%s FAIL-OPEN: non-empty completion validated against empty-bound token ok=%v err=%v", run, ok, err)
	}
	// SINK: empty completion but a real revision against the empty-bound token.
	if ok, err := ValidateToken("", "model@rev-9", tokEmpty, secret); err != nil || ok {
		t.Fatalf("run=%s FAIL-OPEN: wrong revision validated against empty-bound token ok=%v err=%v", run, ok, err)
	}
	t.Logf("run=%s empty evidence is fail-closed (only ('','') validates)", run)
}

// The empty token string ("" — i.e. NO evidence supplied at all) must NEVER
// produce a valid verdict. This is the canonical "missing evidence" fail-open
// probe: an absent token is the most common thing an attacker omits.
// Sink: verdict MUST be (false, ErrMalformedToken), never (true, _).
func TestAdversarial_EmptyTokenNeverValidates(t *testing.T) {
	run := adv(t)
	secret := []byte("anti-cheat-key")

	ok, err := ValidateToken("c", "r", "", secret)
	t.Logf("run=%s empty token -> ok=%v err=%v", run, ok, err)
	if ok {
		t.Fatalf("run=%s FAIL-OPEN: empty/missing token produced a VALID verdict", run)
	}
	if err != ErrMalformedToken {
		t.Fatalf("run=%s empty token want ErrMalformedToken got %v", run, err)
	}
}

// ---------------------------------------------------------------------------
// (b)/(c) "Boundary"/poison analogue: the digest LENGTH guard and the
//         constant-time compare. A truncated, over-length, or all-zero token
//         must be rejected. A naive `==` after a missing length check, or a
//         compare that ignored length, would be the fail-open here.
// ---------------------------------------------------------------------------

// An all-zero digest of the CORRECT length must not validate (it is simply a
// wrong MAC). Probes that the compare is against the real expected MAC, not a
// constant. Sink: verdict MUST be false.
func TestAdversarial_AllZeroTokenRejected(t *testing.T) {
	run := adv(t)
	secret := []byte("k-zero")
	zero := hex.EncodeToString(make([]byte, digestLen)) // 64 hex zeros, correct length
	ok, err := ValidateToken("c", "r", zero, secret)
	t.Logf("run=%s all-zero token -> ok=%v err=%v", run, ok, err)
	if err != nil {
		t.Fatalf("run=%s all-zero token unexpected err=%v (should be well-formed, just wrong)", run, err)
	}
	if ok {
		t.Fatalf("run=%s FAIL-OPEN: all-zero MAC validated", run)
	}
}

// A genuine token TRUNCATED by one byte (62 hex chars) and a genuine token
// EXTENDED by one byte must both be rejected via the length guard, never
// silently accepted by a prefix/relaxed compare. Sink: ErrMalformedToken and
// verdict false in both directions.
func TestAdversarial_LengthConfusionRejected(t *testing.T) {
	run := adv(t)
	secret := []byte("k-len")
	good := GenerateToken("payload", "rev", secret)
	if len(good) != digestLen*2 {
		t.Fatalf("run=%s unexpected good len=%d", run, len(good))
	}

	truncated := good[:len(good)-2] // drop one byte (two hex chars)
	if ok, err := ValidateToken("payload", "rev", truncated, secret); ok || err != ErrMalformedToken {
		t.Fatalf("run=%s FAIL-OPEN/weak-guard: truncated token ok=%v err=%v want false/ErrMalformedToken", run, ok, err)
	}

	overlong := good + "ab" // append one extra byte
	if ok, err := ValidateToken("payload", "rev", overlong, secret); ok || err != ErrMalformedToken {
		t.Fatalf("run=%s FAIL-OPEN/weak-guard: over-length token ok=%v err=%v want false/ErrMalformedToken", run, ok, err)
	}
	t.Logf("run=%s truncated and over-length tokens both fail-closed", run)
}

// Odd-length / non-hex tokens (an attacker feeding garbage) must reject, not
// validate. Sink: ErrMalformedToken, verdict false.
func TestAdversarial_NonHexAndOddLengthRejected(t *testing.T) {
	run := adv(t)
	secret := []byte("k-hex")
	for _, bad := range []string{"x", "abc", "ZZ", strings.Repeat("g", digestLen*2)} {
		ok, err := ValidateToken("c", "r", bad, secret)
		if ok || err != ErrMalformedToken {
			t.Fatalf("run=%s bad token %q -> ok=%v err=%v want false/ErrMalformedToken", run, bad, ok, err)
		}
	}
	t.Logf("run=%s non-hex/odd-length inputs fail-closed", run)
}

// ---------------------------------------------------------------------------
// Cross-key: a token forged under a DIFFERENT secret must not validate. This is
// the core anti-cheat property: without the shared secret an attacker cannot
// mint a passing token. Sink: verdict false.
// ---------------------------------------------------------------------------
func TestAdversarial_CrossSecretRejected(t *testing.T) {
	run := adv(t)
	attackerKey := []byte("attacker-guessed-key")
	realKey := []byte("server-side-real-key")
	completion := "result=PASS"
	revision := "model@rev-7"

	forged := GenerateToken(completion, revision, attackerKey)
	if forged == "" {
		t.Fatalf("run=%s attacker token empty", run)
	}
	// SINK: validate the attacker's token under the REAL key -> must reject.
	ok, err := ValidateToken(completion, revision, forged, realKey)
	t.Logf("run=%s cross-secret forged token under real key -> ok=%v err=%v", run, ok, err)
	if err != nil {
		t.Fatalf("run=%s unexpected err on cross-secret: %v", run, err)
	}
	if ok {
		t.Fatalf("run=%s FAIL-OPEN: token forged under a different secret validated under the real key", run)
	}
}

// Independent oracle: ValidateToken's accept path must agree with a from-scratch
// HMAC-SHA256 over the documented canonical (len-prefixed) encoding. This pins
// the accept verdict to the real cryptographic computation, not a shortcut.
// (Asserts the SINK both ways: oracle-valid -> accept, oracle-invalid -> reject.)
func TestAdversarial_AcceptMatchesIndependentOracle(t *testing.T) {
	run := adv(t)
	secret := []byte("oracle-secret")
	completion := "the answer is 7"
	revision := "rev-XYZ"

	// Independent canonical re-implementation (len-prefixed big-endian).
	canon := func(c, r string) []byte {
		cb, rb := []byte(c), []byte(r)
		buf := make([]byte, 0, 16+len(cb)+len(rb))
		var n [8]byte
		for i := 0; i < 8; i++ {
			n[i] = byte(uint64(len(cb)) >> (8 * (7 - i)))
		}
		buf = append(buf, n[:]...)
		buf = append(buf, cb...)
		for i := 0; i < 8; i++ {
			n[i] = byte(uint64(len(rb)) >> (8 * (7 - i)))
		}
		buf = append(buf, n[:]...)
		buf = append(buf, rb...)
		return buf
	}
	h := hmac.New(sha256.New, secret)
	h.Write(canon(completion, revision))
	want := hex.EncodeToString(h.Sum(nil))

	got := GenerateToken(completion, revision, secret)
	if got != want {
		t.Fatalf("run=%s token disagrees with independent oracle got=%s want=%s", run, got, want)
	}
	if ok, err := ValidateToken(completion, revision, want, secret); err != nil || !ok {
		t.Fatalf("run=%s oracle-valid token rejected ok=%v err=%v", run, ok, err)
	}
	// And the oracle for a DIFFERENT pair must be rejected (no fail-open).
	hBad := hmac.New(sha256.New, secret)
	hBad.Write(canon(completion, "rev-OTHER"))
	wrong := hex.EncodeToString(hBad.Sum(nil))
	if ok, err := ValidateToken(completion, revision, wrong, secret); err != nil || ok {
		t.Fatalf("run=%s FAIL-OPEN: oracle token for a different revision validated ok=%v err=%v", run, ok, err)
	}
	t.Logf("run=%s accept verdict matches independent HMAC oracle both directions", run)
}

// ---------------------------------------------------------------------------
// (d) IDENTITY / determinism + concurrency: identical inputs yield identical
//     tokens; ValidateToken is safe under concurrent use with no unguarded
//     shared state (run under -race). A data race here would be the
//     "unguarded shared state" finding.
// ---------------------------------------------------------------------------
func TestAdversarial_DeterministicAndRaceFree(t *testing.T) {
	run := adv(t)
	secret := []byte("concurrent-key")
	completion := "deterministic"
	revision := "rev-C"
	want := GenerateToken(completion, revision, secret)

	const workers = 64
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			g := GenerateToken(completion, revision, secret)
			if g != want {
				errs <- "nondeterministic token under concurrency"
				return
			}
			ok, err := ValidateToken(completion, revision, g, secret)
			if err != nil || !ok {
				errs <- "concurrent genuine validate failed"
				return
			}
			// A concurrent forged attempt must still be rejected.
			if okF, _ := ValidateToken("forged", revision, g, secret); okF {
				errs <- "FAIL-OPEN under concurrency: forged validated"
			}
		}()
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Fatalf("run=%s %s", run, msg)
	}
	t.Logf("run=%s %d workers: deterministic, race-free, fail-closed", run, workers)
}
