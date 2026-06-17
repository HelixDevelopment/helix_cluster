package anticheat

// Second-wave adversarial sink-side probes for the anticheat HMAC boundary.
//
// The first wave (anticheat_adversarial_test.go) pinned the obvious fail-open
// vectors: empty/zero evidence, missing token, length confusion, non-hex,
// cross-secret, the independent-oracle accept path, and determinism/-race.
//
// This wave pins contract surface those probes did NOT isolate, all asserted on
// the SINK side (the actual (bool,err) verdict), all written to PASS on the
// current (correct, fail-closed) prod and to BITE on a plausible regression:
//
//   1. FIELD-SWAP confusion: a token bound to (completion=A, revision=B) must
//      NOT validate the swapped claim (completion=B, revision=A) when A!=B.
//      This is the anti-cheat-specific footgun where an implementation mixes up
//      which value is "the result" vs "the model revision" (e.g. concatenates
//      without the per-field length frame, or HMACs revision||completion on one
//      side). It is distinct from forged-completion / wrong-revision because
//      BOTH supplied values are individually genuine — only their ROLES swap.
//
//   2. MULTIBYTE (UTF-8) injectivity: the canonical frame length-prefixes the
//      BYTE length, not the rune length. A regression to rune-length framing
//      (e.g. len([]rune(x))) would let a 2-byte multibyte completion collide
//      with a differently-split byte sequence. Sink: distinct pairs -> distinct
//      tokens AND cross-validation rejected.
//
//   3. HEX CASE-INSENSITIVITY of the accept path: a genuine token re-encoded in
//      UPPERCASE hex must STILL validate, because validation decodes the hex and
//      constant-time-compares the DECODED bytes (hmac.Equal), never the hex
//      strings. A regression to a raw string compare (tokenHex == expectedHex)
//      would fail-CLOSED here (wrongly reject a genuine upper-case token) — a
//      correctness/availability defect this probe pins. (It also documents that
//      the boundary is byte-level, not string-level.)
//
//   4. SECRET is genuinely mixed in on GENERATE: two different secrets over the
//      same (completion,revision) MUST yield different tokens, and a token from
//      secret-A must not validate under secret-B. Wave 1 covered cross-secret on
//      the validate side via a forged token; this pins the generate side and the
//      no-fixed-key property directly.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// adv2 reuses the forensic run tag helper from wave 1.
func adv2(t *testing.T) string { t.Helper(); return newRunUUID(t) }

// upperHex uppercases the a-f hex nibbles of a token, leaving it valid hex of
// identical length but in the OTHER case than GenerateToken emits.
func upperHex(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'f' {
			b[i] -= 32
		}
	}
	return string(b)
}

// (1) FIELD-SWAP confusion must be REJECTED.
//
// Mutation that BITES: HMACing a role-agnostic encoding (e.g. completion bound
// the same as revision, or revision||completion vs completion||revision) makes
// the swapped pair validate. The per-field length frame plus fixed field order
// in canonical() prevents this; here we assert the verdict on the swapped claim
// is false while the genuine and the genuine-swapped-token are both correct.
func TestAdversarial2_FieldSwapRejected(t *testing.T) {
	run := adv2(t)
	secret := []byte("role-binding-key")
	completion := "RESULT-AAA"   // the value that is the "result"
	revision := "B"              // the value that is the "model revision"

	tokAB := GenerateToken(completion, revision, secret)
	if tokAB == "" {
		t.Fatalf("run=%s token empty", run)
	}
	// Baseline: genuine (A,B) validates (else the negative is vacuous).
	if ok, err := ValidateToken(completion, revision, tokAB, secret); err != nil || !ok {
		t.Fatalf("run=%s genuine (A,B) baseline failed ok=%v err=%v", run, ok, err)
	}

	// SINK: the swapped claim (revision-as-completion, completion-as-revision)
	// against the (A,B)-bound token MUST be rejected.
	if ok, err := ValidateToken(revision, completion, tokAB, secret); err != nil || ok {
		t.Fatalf("run=%s FAIL-OPEN: field-swapped claim (B,A) validated against (A,B) token ok=%v err=%v", run, ok, err)
	}

	// And the genuinely-swapped token must differ from the original (no role
	// collision in the MAC output itself).
	tokBA := GenerateToken(revision, completion, secret)
	if tokBA == tokAB {
		t.Fatalf("run=%s (A,B) and (B,A) produced the SAME token; field roles not bound", run)
	}
	// The swapped token validates ONLY its own swapped pair, not the original.
	if ok, err := ValidateToken(completion, revision, tokBA, secret); err != nil || ok {
		t.Fatalf("run=%s FAIL-OPEN: (B,A) token validated the (A,B) claim ok=%v err=%v", run, ok, err)
	}
	t.Logf("run=%s field roles are bound; swap is fail-closed", run)
}

// (2) MULTIBYTE / UTF-8 injectivity: byte-length framing, not rune-length.
//
// "é" is the two bytes 0xC3 0xA9. We compare the pair ("é","x") against a pair
// whose completion is the single ASCII byte that, under a buggy rune-length
// frame, could be confused. Concretely we assert two boundary-shifted pairs over
// the SAME total byte content produce distinct tokens and reject cross-validation.
//
// Mutation that BITES: framing len([]rune(c)) instead of len(c) (or len in code
// points) collapses the byte boundary -> these pairs collide.
func TestAdversarial2_MultibyteInjective(t *testing.T) {
	run := adv2(t)
	secret := []byte("utf8-frame-key")

	// Two distinct (completion,revision) pairs whose RAW BYTES, naively
	// concatenated, are identical: "é"+"x" == 0xC3 0xA9 0x78 and
	// "\xc3" + "\xa9x" == 0xC3 0xA9 0x78. Only the byte-length frame separates
	// them; a rune-aware frame would mis-measure the leading multibyte field.
	c1, r1 := "é", "x"      // completion = 2 bytes (0xC3,0xA9), revision = "x"
	c2, r2 := "\xc3", "\xa9x" // completion = 1 byte (0xC3), revision = 2 bytes

	t1 := GenerateToken(c1, r1, secret)
	t2 := GenerateToken(c2, r2, secret)
	if t1 == t2 {
		t.Fatalf("run=%s multibyte boundary-shifted pairs collided (rune-length framing?) t=%s", run, t1)
	}
	// Cross-validation in both directions must reject.
	if ok, err := ValidateToken(c2, r2, t1, secret); err != nil || ok {
		t.Fatalf("run=%s FAIL-OPEN: pair-2 validated pair-1 token ok=%v err=%v", run, ok, err)
	}
	if ok, err := ValidateToken(c1, r1, t2, secret); err != nil || ok {
		t.Fatalf("run=%s FAIL-OPEN: pair-1 validated pair-2 token ok=%v err=%v", run, ok, err)
	}
	t.Logf("run=%s multibyte framing is byte-exact and injective", run)
}

// (3) HEX CASE-INSENSITIVITY of the accept path.
//
// A genuine token, re-encoded in UPPERCASE hex, must still validate: validation
// decodes the hex and compares the DECODED digest bytes via hmac.Equal. This
// pins that the compare is byte-level (hmac.Equal), never a hex STRING compare.
//
// Mutation that BITES: replacing `hmac.Equal(supplied, expected)` with
// `tokenHex == hex.EncodeToString(expected)` (a string compare) -> the
// uppercase genuine token is wrongly REJECTED (fail-closed regression). This
// also matters because hex.DecodeString accepts mixed case, so an honest client
// emitting upper-case hex must not be locked out.
func TestAdversarial2_UpperCaseHexStillValidates(t *testing.T) {
	run := adv2(t)
	secret := []byte("case-key")
	completion := "honest result value"
	revision := "model@rev-CASE"

	tok := GenerateToken(completion, revision, secret)
	UP := upperHex(tok)
	if UP == tok {
		t.Fatalf("run=%s token had no a-f nibble to upcase (retry would be flaky); tok=%s", run, tok)
	}
	// Decoded bytes are identical, so the genuine UPPER-case token MUST validate.
	ok, err := ValidateToken(completion, revision, UP, secret)
	t.Logf("run=%s uppercase genuine token -> ok=%v err=%v", run, ok, err)
	if err != nil {
		t.Fatalf("run=%s uppercase genuine token errored: %v (string-compare regression?)", run, err)
	}
	if !ok {
		t.Fatalf("run=%s genuine UPPERCASE-hex token wrongly REJECTED (validate must decode+hmac.Equal, not string-compare)", run)
	}

	// Cross-check against an independent oracle: the decoded UP equals the raw MAC.
	want := hmacMAC(t, completion, revision, secret)
	got, derr := hex.DecodeString(UP)
	if derr != nil {
		t.Fatalf("run=%s upper token not valid hex: %v", run, derr)
	}
	if !hmac.Equal(got, want) {
		t.Fatalf("run=%s decoded uppercase token != independent MAC", run)
	}
}

// (4) SECRET is mixed into GENERATE; no fixed/zero key.
//
// Two distinct secrets over the SAME (completion,revision) must yield DISTINCT
// tokens, and a token minted under secret-A must not validate under secret-B.
//
// Mutation that BITES: dropping the secret from the HMAC (or pinning a constant
// key) makes both tokens equal and lets secret-B validate secret-A's token ->
// the core anti-cheat secret is meaningless.
func TestAdversarial2_SecretBoundOnGenerate(t *testing.T) {
	run := adv2(t)
	completion := "result=PASS"
	revision := "model@rev-7"
	keyA := []byte("server-key-alpha")
	keyB := []byte("server-key-bravo")

	tA := GenerateToken(completion, revision, keyA)
	tB := GenerateToken(completion, revision, keyB)
	if tA == "" || tB == "" {
		t.Fatalf("run=%s empty token(s) tA=%q tB=%q", run, tA, tB)
	}
	if tA == tB {
		t.Fatalf("run=%s FAIL-OPEN: same token under different secrets (secret not bound / fixed key)", run)
	}
	// A's token must validate under A and REJECT under B.
	if ok, err := ValidateToken(completion, revision, tA, keyA); err != nil || !ok {
		t.Fatalf("run=%s tA under keyA baseline failed ok=%v err=%v", run, ok, err)
	}
	if ok, err := ValidateToken(completion, revision, tA, keyB); err != nil || ok {
		t.Fatalf("run=%s FAIL-OPEN: keyA token validated under keyB ok=%v err=%v", run, ok, err)
	}
	t.Logf("run=%s secret is bound on generate; cross-secret fail-closed", run)
}

// hmacMAC is an independent (test-local) recomputation of the raw MAC over the
// documented canonical (8-byte big-endian length-prefixed) encoding, used as an
// oracle so the assertion does not depend on the package's own mac().
func hmacMAC(t *testing.T, completion, revision string, secret []byte) []byte {
	t.Helper()
	cb, rb := []byte(completion), []byte(revision)
	buf := make([]byte, 0, 16+len(cb)+len(rb))
	var n [8]byte
	put := func(v uint64) {
		for i := 0; i < 8; i++ {
			n[i] = byte(v >> (8 * (7 - i)))
		}
		buf = append(buf, n[:]...)
	}
	put(uint64(len(cb)))
	buf = append(buf, cb...)
	put(uint64(len(rb)))
	buf = append(buf, rb...)
	h := hmac.New(sha256.New, secret)
	h.Write(buf)
	return h.Sum(nil)
}
