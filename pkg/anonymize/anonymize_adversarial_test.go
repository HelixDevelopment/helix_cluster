package anonymize

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// hexLen is the length of a lowercase-hex SHA-256 digest (64 chars).
const hexLen = 2 * sha256.Size

// adversarialSecrets is a battery of real-world PII / secret shapes. Each is a
// single whitespace-free token (so it survives SplitBlocks as ONE block) plus a
// couple of punctuation-attached variants. The privacy contract is absolute:
// NONE of these raw strings may appear anywhere in the anonymized output.
var adversarialSecrets = []string{
	"alice.smith@example.com",           // email
	"ALICE.SMITH@EXAMPLE.COM",           // email, upper-case variant
	"john.doe+tag@sub.corp.co.uk",       // email with plus-tag + subdomain
	"192.168.1.254",                     // IPv4
	"2001:db8::dead:beef",               // IPv6
	"ssn:123-45-6789",                   // SSN-shaped, label-attached
	"+1-415-555-0132",                   // phone E.164-ish
	"4111111111111111",                  // PAN (credit card)
	"sk_live_abcd1234EFGH5678wxyz",      // secret key
	"AKIAIOSFODNN7EXAMPLE",              // AWS access key id
	"ghp_AbCdEfGhIjKlMnOpQrStUvWxYz012", // GitHub PAT
	"Bearer.eyJhbGciOiJI.payload.sig",   // JWT-ish bearer token
	"password=Hunter2!",                 // password assignment
	"name=Jelena.Petrović",              // name field, non-ASCII
}

// assertNoLeak fails if any raw secret survives anywhere in the joined output,
// and confirms each emitted token is a fixed-width hex digest (no passthrough).
func assertNoLeak(t *testing.T, runUUID string, out []string, secrets []string) {
	t.Helper()
	joined := strings.Join(out, " ") // digests are hex so spaces cannot be part of a secret block
	flat := strings.Join(out, "")
	for _, tok := range out {
		if len(tok) != hexLen {
			t.Fatalf("run=%s emitted token %q len=%d want=%d (raw passthrough?)", runUUID, tok, len(tok), hexLen)
		}
	}
	for _, s := range secrets {
		if s == "" {
			continue
		}
		if strings.Contains(joined, s) {
			t.Fatalf("run=%s PRIVACY LEAK: raw secret %q present in output %q", runUUID, s, joined)
		}
		// Also guard against a secret reconstructable across block boundaries.
		if strings.Contains(flat, s) {
			t.Fatalf("run=%s PRIVACY LEAK: raw secret %q reconstructable across blocks in %q", runUUID, s, flat)
		}
	}
}

// TestAnonymize_PIIBatteryNeverLeaks feeds every adversarial secret (alone and
// embedded among benign tokens) and asserts the raw value never survives.
//
// Mutation that bites: a no-op redact (out[i] = block) or a "redact only the
// first block" change makes one of these raw secrets reappear -> FAIL.
func TestAnonymize_PIIBatteryNeverLeaks(t *testing.T) {
	runUUID := newRunUUID(t, "pii-battery")
	a := New("prompt-v1")
	tenantKey := []byte("tenant-alpha-secret-key")

	for _, secret := range adversarialSecrets {
		// Embed the secret among benign neighbours so a "redact-first-only" or
		// "redact-last-only" bug is caught regardless of position.
		prompt := "lorem ipsum " + secret + " dolor sit " + secret + " amet"
		t.Logf("run=%s before: secret=%q prompt=%q", runUUID, secret, prompt)

		out := a.Anonymize(tenantKey, prompt)
		t.Logf("run=%s after: out=%v", runUUID, out)

		blocks := SplitBlocks(prompt)
		if len(out) != len(blocks) {
			t.Fatalf("run=%s out=%d blocks=%d", runUUID, len(out), len(blocks))
		}
		// The secret appears twice in the prompt -> must dedup to one digest
		// (positions 2 and 5), proving every occurrence is hashed, not just one.
		if out[2] != out[5] {
			t.Fatalf("run=%s both occurrences of %q must hash identically: %q vs %q",
				runUUID, secret, out[2], out[5])
		}
		assertNoLeak(t, runUUID, out, []string{secret})
		// And the digest must NOT equal the secret (sanity vs raw passthrough).
		for _, tok := range out {
			if tok == secret {
				t.Fatalf("run=%s PRIVACY LEAK: digest equals raw secret %q", runUUID, secret)
			}
		}
	}
}

// TestAnonymize_MultiOccurrenceAllRedacted hammers the "replace-first" bug
// class directly: the SAME secret repeated many times must be hashed at EVERY
// position, and the raw secret must be absent.
//
// Mutation that bites: replace-first (only out[0] hashed, rest = raw block)
// leaves the raw secret in out[1..] -> assertNoLeak FAILs with leaked PII.
func TestAnonymize_MultiOccurrenceAllRedacted(t *testing.T) {
	runUUID := newRunUUID(t, "multi-occurrence")
	a := New("audit-v2")
	tenantKey := []byte("tenant-omega-secret-key")
	secret := "victim@hospital.example"

	prompt := strings.TrimSpace(strings.Repeat(secret+" ", 8))
	out := a.Anonymize(tenantKey, prompt)
	t.Logf("run=%s before: prompt=%q after: out=%v", runUUID, prompt, out)

	if len(out) != 8 {
		t.Fatalf("run=%s out=%d want=8", runUUID, len(out))
	}
	for i := 1; i < len(out); i++ {
		if out[i] != out[0] {
			t.Fatalf("run=%s occurrence %d digest=%q != %q (some occurrence not redacted consistently)",
				runUUID, i, out[i], out[0])
		}
	}
	assertNoLeak(t, runUUID, out, []string{secret})
}

// TestAnonymize_CaseAndFormatVariantsAllRedacted asserts there is no format or
// case "slip-past": each variant is independently hashed and absent. It also
// confirms case-sensitivity (distinct casings -> distinct digests), so the
// scheme is not silently lower-casing (which would corrupt correlation).
//
// Mutation that bites: a no-op redact leaves a variant raw -> FAIL.
func TestAnonymize_CaseAndFormatVariantsAllRedacted(t *testing.T) {
	runUUID := newRunUUID(t, "case-variants")
	a := New("prompt-v1")
	tenantKey := []byte("tenant-alpha-secret-key")

	variants := []string{
		"Token-ABCDEF", "token-abcdef", "TOKEN-ABCDEF",
		"User@Host.COM", "user@host.com",
	}
	prompt := strings.Join(variants, " ")
	out := a.Anonymize(tenantKey, prompt)
	t.Logf("run=%s before: prompt=%q after: out=%v", runUUID, prompt, out)

	assertNoLeak(t, runUUID, out, variants)

	// Distinct casings must NOT collapse to the same digest (no lower-casing).
	if out[0] == out[1] || out[1] == out[2] || out[0] == out[2] {
		t.Fatalf("run=%s case variants collapsed to same digest: %v", runUUID, out[:3])
	}
	if out[3] == out[4] {
		t.Fatalf("run=%s email case variants collapsed: %q == %q", runUUID, out[3], out[4])
	}
}

// TestAnonymize_DomainSeparation pins the documented namespace guarantee: the
// SAME block under the SAME tenant key but DIFFERENT domains must NOT collide.
//
// Mutation that bites: dropping the domain from hashBlock (e.g. not writing
// a.domain into the MAC) makes both domains produce the IDENTICAL digest -> FAIL.
func TestAnonymize_DomainSeparation(t *testing.T) {
	runUUID := newRunUUID(t, "domain-sep")
	tenantKey := []byte("tenant-alpha-secret-key")
	block := "correlatable-secret-token"

	d1 := New("prompt-v1").Anonymize(tenantKey, block)
	d2 := New("audit-v2").Anonymize(tenantKey, block)
	t.Logf("run=%s d1=%v d2=%v", runUUID, d1, d2)

	if len(d1) != 1 || len(d2) != 1 {
		t.Fatalf("run=%s unexpected block counts: %v %v", runUUID, d1, d2)
	}
	if d1[0] == d2[0] {
		t.Fatalf("run=%s domain separation broken: %q == %q (domain not mixed into MAC)",
			runUUID, d1[0], d2[0])
	}
	// Independent oracle: each must match its own domain's framed HMAC.
	if want := independentDigest("prompt-v1", tenantKey, block); d1[0] != want {
		t.Fatalf("run=%s domain prompt-v1 digest=%q want=%q", runUUID, d1[0], want)
	}
	if want := independentDigest("audit-v2", tenantKey, block); d2[0] != want {
		t.Fatalf("run=%s domain audit-v2 digest=%q want=%q", runUUID, d2[0], want)
	}
}

// TestAnonymize_LengthFramingNoBoundaryCollision pins the documented framing
// guarantee. Without length framing, (domain="ab", block="cX") and
// (domain="a", block="bcX") concatenate to the same MAC input and collide.
//
// Mutation that bites: removing writeFramed length prefixes (concatenating raw
// domain+block) makes these two configurations produce the SAME digest -> FAIL.
func TestAnonymize_LengthFramingNoBoundaryCollision(t *testing.T) {
	runUUID := newRunUUID(t, "framing")
	tenantKey := []byte("tenant-alpha-secret-key")

	// Shared concatenation "abcX" split at two different boundaries.
	d1 := New("ab").Anonymize(tenantKey, "cX")
	d2 := New("a").Anonymize(tenantKey, "bcX")
	t.Logf("run=%s d1=%v d2=%v", runUUID, d1, d2)

	if d1[0] == d2[0] {
		t.Fatalf("run=%s boundary-ambiguity collision: ('ab','cX') == ('a','bcX') digest %q (framing dropped)",
			runUUID, d1[0])
	}
}

// TestAnonymize_EmptyAndWhitespace asserts no panic and no spurious output on
// empty / whitespace-only / nil-key inputs, and no over-redaction of nothing.
func TestAnonymize_EmptyAndWhitespace(t *testing.T) {
	runUUID := newRunUUID(t, "empty")
	a := New("prompt-v1")

	cases := []struct {
		name string
		key  []byte
		text string
	}{
		{"empty-text", []byte("k"), ""},
		{"spaces-only", []byte("k"), "   \t\n  "},
		{"nil-key-empty", nil, ""},
		{"nil-key-text", nil, "secret token"},
		{"empty-key-text", []byte{}, "secret token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := a.Anonymize(tc.key, tc.text)
			t.Logf("run=%s case=%s out=%v", runUUID, tc.name, out)
			wantN := len(SplitBlocks(tc.text))
			if len(out) != wantN {
				t.Fatalf("run=%s case=%s out=%d want=%d", runUUID, tc.name, len(out), wantN)
			}
			for _, tok := range out {
				if len(tok) != hexLen {
					t.Fatalf("run=%s case=%s token %q len=%d want=%d", runUUID, tc.name, tok, len(tok), hexLen)
				}
			}
			// Even with a nil/empty key the raw text must not survive.
			assertNoLeak(t, runUUID, out, SplitBlocks(tc.text))
		})
	}
}

// TestAnonymize_BenignBaseline is the over-redaction guard: benign,
// non-PII text yields exactly one digest per word with no data lost or
// merged. We verify the block COUNT is preserved (no silent dropping) and
// every digest is a well-formed token. This catches a mutation that drops a
// category/position (e.g. skipping a block), which would shrink the output.
func TestAnonymize_BenignBaseline(t *testing.T) {
	runUUID := newRunUUID(t, "baseline")
	a := New("prompt-v1")
	tenantKey := []byte("tenant-alpha-secret-key")

	prompt := "the quick brown fox jumps over the lazy dog"
	blocks := SplitBlocks(prompt) // 9 words, "the" repeated
	out := a.Anonymize(tenantKey, prompt)
	t.Logf("run=%s before: prompt=%q blocks=%v after: out=%v", runUUID, prompt, blocks, out)

	if len(out) != len(blocks) {
		t.Fatalf("run=%s over/under-redaction: out=%d blocks=%d", runUUID, len(out), len(blocks))
	}
	// "the" appears at index 0 and 6 -> same digest (consistency preserved).
	if out[0] != out[6] {
		t.Fatalf("run=%s repeated benign word 'the' inconsistent: %q vs %q", runUUID, out[0], out[6])
	}
	// Distinct benign words -> distinct digests (no over-merge / data loss).
	distinct := map[string]bool{}
	for i, tok := range out {
		if i == 6 { // second "the"
			continue
		}
		if distinct[tok] {
			t.Fatalf("run=%s distinct benign word at %d (%q) collided: digest %q", runUUID, i, blocks[i], tok)
		}
		distinct[tok] = true
	}
	// Sanity: a digest must be valid hex (decodes cleanly).
	if _, err := hex.DecodeString(out[0]); err != nil {
		t.Fatalf("run=%s digest not valid hex: %v", runUUID, err)
	}
}
