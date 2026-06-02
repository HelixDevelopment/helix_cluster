package anticheat

import (
	"crypto/rand"
	"strings"
	"testing"
)

// newRunUUID returns a random hex run identifier for forensic logging.
func newRunUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	const hexd = "0123456789abcdef"
	out := make([]byte, 32)
	for i, x := range b {
		out[2*i] = hexd[x>>4]
		out[2*i+1] = hexd[x&0x0f]
	}
	return string(out)
}

// flipLastHexNibble flips the last hex nibble of a token to tamper it while
// keeping it valid hex of the same length.
func flipLastHexNibble(tok string) string {
	if tok == "" {
		return tok
	}
	last := tok[len(tok)-1]
	var rep byte
	if last == '0' {
		rep = '1'
	} else {
		rep = '0'
	}
	return tok[:len(tok)-1] + string(rep)
}

// Clause 1: Genuine token generated for (completion, revision) VALIDATES.
// Mutation: if GenerateToken/ValidateToken stop agreeing on the canonical
// encoding (e.g. revision dropped from the MAC on one side), this fails.
func TestGenuineTokenValidates(t *testing.T) {
	run := newRunUUID(t)
	secret := []byte("rev-binding-secret-key-v2")
	completion := "the capital of France is Paris"
	revision := "model-7b@rev-2026-06-02"

	t.Logf("run=%s before: generating token completion=%q revision=%q", run, completion, revision)
	tok := GenerateToken(completion, revision, secret)
	if tok == "" {
		t.Fatalf("run=%s expected non-empty token", run)
	}
	if len(tok) != digestLen*2 {
		t.Fatalf("run=%s token hex len=%d want=%d", run, len(tok), digestLen*2)
	}

	ok, err := ValidateToken(completion, revision, tok, secret)
	t.Logf("run=%s after: validate genuine -> ok=%v err=%v", run, ok, err)
	if err != nil {
		t.Fatalf("run=%s unexpected error: %v", run, err)
	}
	if !ok {
		t.Fatalf("run=%s genuine token did NOT validate", run)
	}
}

// Clause 2: Validating the genuine token against a DIFFERENT completion value
// is REJECTED (forged completion).
// Mutation: if the MAC ignores completion, the forged value would wrongly
// validate and this fails.
func TestForgedCompletionRejected(t *testing.T) {
	run := newRunUUID(t)
	secret := []byte("rev-binding-secret-key-v2")
	revision := "model-7b@rev-2026-06-02"
	genuine := "score: 42"
	forged := "score: 99" // attacker swaps the result value

	tok := GenerateToken(genuine, revision, secret)
	t.Logf("run=%s before: genuine token for %q = %s", run, genuine, tok)

	// Sanity: the genuine completion must validate (else the negative case is vacuous).
	if ok, err := ValidateToken(genuine, revision, tok, secret); err != nil || !ok {
		t.Fatalf("run=%s genuine baseline failed ok=%v err=%v", run, ok, err)
	}

	ok, err := ValidateToken(forged, revision, tok, secret)
	t.Logf("run=%s after: validate forged completion %q -> ok=%v err=%v", run, forged, ok, err)
	if err != nil {
		t.Fatalf("run=%s unexpected error: %v", run, err)
	}
	if ok {
		t.Fatalf("run=%s FORGED completion wrongly validated", run)
	}
}

// Clause 3: Validating against a DIFFERENT revision is REJECTED, and a tampered
// (flipped-hex) token is REJECTED.
// Mutation: binding the HMAC over completion ONLY (ignoring revision) makes the
// wrong-revision sub-case wrongly validate -> this fails. The tamper sub-case
// guards the constant-time compare path.
func TestWrongRevisionAndTamperedTokenRejected(t *testing.T) {
	run := newRunUUID(t)
	secret := []byte("rev-binding-secret-key-v2")
	completion := "deterministic output payload"
	genuineRev := "model-7b@rev-A"
	wrongRev := "model-7b@rev-B" // same model, different revision

	tok := GenerateToken(completion, genuineRev, secret)
	t.Logf("run=%s before: token bound to rev=%q = %s", run, genuineRev, tok)

	// Baseline genuine must validate.
	if ok, err := ValidateToken(completion, genuineRev, tok, secret); err != nil || !ok {
		t.Fatalf("run=%s genuine baseline failed ok=%v err=%v", run, ok, err)
	}

	// Sub-case A: wrong revision rejected.
	okRev, errRev := ValidateToken(completion, wrongRev, tok, secret)
	t.Logf("run=%s after: validate wrong rev=%q -> ok=%v err=%v", run, wrongRev, okRev, errRev)
	if errRev != nil {
		t.Fatalf("run=%s unexpected error on wrong revision: %v", run, errRev)
	}
	if okRev {
		t.Fatalf("run=%s WRONG revision wrongly validated (revision not bound into MAC)", run)
	}

	// Sub-case B: tampered token (flipped last hex nibble) rejected.
	tampered := flipLastHexNibble(tok)
	if tampered == tok {
		t.Fatalf("run=%s tamper did not change token", run)
	}
	okTam, errTam := ValidateToken(completion, genuineRev, tampered, secret)
	t.Logf("run=%s after: validate tampered token=%s -> ok=%v err=%v", run, tampered, okTam, errTam)
	if errTam != nil {
		t.Fatalf("run=%s unexpected error on tampered token: %v", run, errTam)
	}
	if okTam {
		t.Fatalf("run=%s TAMPERED token wrongly validated", run)
	}
}

// Supporting: a malformed (non-hex or wrong-length) token yields ErrMalformedToken,
// and an empty secret yields ErrEmptySecret. Guards the input-validation branch.
func TestMalformedAndEmptySecret(t *testing.T) {
	run := newRunUUID(t)
	secret := []byte("k")
	completion := "x"
	revision := "r"
	t.Logf("run=%s before: malformed/empty-secret checks", run)

	if _, err := ValidateToken(completion, revision, "zzzz", secret); err != ErrMalformedToken {
		t.Fatalf("run=%s non-hex want ErrMalformedToken got %v", run, err)
	}
	if _, err := ValidateToken(completion, revision, "abcd", secret); err != ErrMalformedToken {
		t.Fatalf("run=%s short hex want ErrMalformedToken got %v", run, err)
	}
	good := GenerateToken(completion, revision, secret)
	if _, err := ValidateToken(completion, revision, good, nil); err != ErrEmptySecret {
		t.Fatalf("run=%s empty secret want ErrEmptySecret got %v", run, err)
	}
	if GenerateToken(completion, revision, nil) != "" {
		t.Fatalf("run=%s empty-secret generate should yield empty token", run)
	}
	t.Logf("run=%s after: malformed/empty-secret checks passed", run)
}

// Supporting: the canonical length-prefix encoding is injective over the
// (completion, revision) PAIR, so boundary-ambiguous pairs produce DIFFERENT
// tokens and cross-validation is rejected. A naive completion+revision
// concatenation (no length framing at all) would collide on these pairs.
//
// Scope note (accuracy, per adversarial review): a single (completion,
// revision)-pair test CANNOT isolate "drop exactly one of the two length
// prefixes", because the remaining prefix re-disambiguates the pair — e.g.
// dropping only the completion prefix still leaves the revision-length prefix
// to separate ("ab","c") from ("a","bc"). The per-field binding of the MAC is
// therefore pinned where it is actually testable:
//   - completion bound into the MAC -> TestForgedCompletionRejected
//   - revision   bound into the MAC -> TestWrongRevisionAndTamperedTokenRejected
// This test pins the remaining property: the PAIR encoding is injective across
// the field boundary in both directions (completion-side and revision-side
// shifts), which a total loss of length framing would violate.
//
// Mutation: removing the length framing entirely from canonical() (naive
// cb||rb) collapses Family 1 to a single value -> t1 == t2 -> this fails.
func TestCanonicalEncodingIsInjective(t *testing.T) {
	run := newRunUUID(t)
	secret := []byte("sep-secret")

	// Family 1: completion-boundary ambiguity over constant "abc".
	t1 := GenerateToken("ab", "c", secret)
	t2 := GenerateToken("a", "bc", secret)
	t.Logf("run=%s family1: t1=%s t2=%s", run, t1, t2)
	if t1 == t2 {
		t.Fatalf("run=%s family1 boundary-ambiguous pairs collided; completion prefix not load-bearing", run)
	}
	// Cross-validation must be rejected (token for (ab,c) must not validate (a,bc)).
	if ok, err := ValidateToken("a", "bc", t1, secret); err != nil || ok {
		t.Fatalf("run=%s family1 cross-boundary token wrongly validated ok=%v err=%v", run, ok, err)
	}

	// Family 2: revision-boundary ambiguity over constant "xab". The completion
	// field content differs ("x" vs "xa"), so this pair is genuinely distinct
	// only because the revision field cannot absorb the trailing "a". Tokens
	// must differ AND cross-validation must be rejected.
	t3 := GenerateToken("x", "ab", secret)
	t4 := GenerateToken("xa", "b", secret)
	t.Logf("run=%s family2: t3=%s t4=%s", run, t3, t4)
	if t3 == t4 {
		t.Fatalf("run=%s family2 boundary-ambiguous pairs collided; encoding not injective", run)
	}
	if ok, err := ValidateToken("xa", "b", t3, secret); err != nil || ok {
		t.Fatalf("run=%s family2 cross-boundary token wrongly validated ok=%v err=%v", run, ok, err)
	}

	if !strings.HasPrefix(t1, "") { // keep strings import meaningful
		t.Fatalf("run=%s unreachable", run)
	}
}
