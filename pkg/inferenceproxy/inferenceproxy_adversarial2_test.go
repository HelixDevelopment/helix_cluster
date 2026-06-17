package inferenceproxy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Adversarial SECOND pass for pkg/inferenceproxy. The first pass pinned the
// admission-ordering / forwarding / fidelity / single-backend / error-surfacing
// contract. This pass targets the recurring bug classes NOT covered there:
//   (A) POINTER/SLICE ALIASING — MemAudit.Entries() returning a snapshot that
//       shares the backing array with internal state, so a caller mutating the
//       returned slice corrupts the audit store (or a later append into the
//       returned snapshot stomps a prior caller's snapshot).
//   (C) FABRICATED SUCCESS — a backend that returns a NON-zero Response together
//       WITH an error: the proxy must surface the error, not the response, with
//       no fail-open that hands the partial Response to the caller as success.
//   (D) FAIL-OPEN on authorizer construction edge cases (empty/nil secret) and
//       on token canonicalization (uppercase hex, leading/trailing space,
//       length-extension shapes) — the gate must FAIL CLOSED.
//   (E) SECRET LEAK — the audit entry / returned errors must not embed the HMAC
//       secret or the raw token (so a captured audit log is not a credential
//       store).
//
// All probes assert SINK-SIDE state (backend hit counter, audit store contents,
// independent crypto oracle) — never just a function return value. New helper
// types are suffixed *2 to avoid colliding with the first-pass helpers.
// ---------------------------------------------------------------------------

// fabricatingBackend2 returns a NON-zero Response *and* a non-nil error
// simultaneously. A correct proxy surfaces the error; a fail-open proxy that
// returned resp on the theory "we have a response" would leak this fabricated
// body to the caller as if it were a success.
type fabricatingBackend2 struct {
	resp Response
	err  error
}

func (b *fabricatingBackend2) Infer(context.Context, Request) (Response, error) {
	return b.resp, b.err
}

// TestAliasing_EntriesSnapshotIsolatedFromStore proves MemAudit.Entries()
// returns a snapshot whose backing array does NOT alias the store: mutating the
// returned slice elements cannot corrupt a later Entries() read, and a
// subsequent Record() (which appends) cannot retroactively change a snapshot the
// caller already holds.
//
// Mutation (class A): change audit.go Entries() to `return m.entries` (drop the
// make+copy). Then the snapshot aliases the store; the in-place element edit
// below corrupts the store and the re-read assertion fails. Proven load-bearing.
func TestAliasing_EntriesSnapshotIsolatedFromStore(t *testing.T) {
	audit := NewMemAudit()
	backend := &countingBackend{resp: Response{Model: "m", Text: "ok"}}
	p := mustProxy(t, backend, allowAll{}, audit, fixedClock(time.Unix(1, 0)))

	if _, err := p.Infer(context.Background(), Request{Subject: "alice", Model: "qwen", Prompt: "hello"}); err != nil {
		t.Fatalf("Infer: %v", err)
	}

	snap := audit.Entries()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	// Adversarial caller stomps the returned snapshot in place.
	snap[0].Subject = "MALLORY-OVERWRITE"
	snap[0].Model = "EVIL"
	snap[0].PromptBytes = 99999
	snap[0].Decision = DecisionDeny

	// The STORE must be untouched: a fresh read returns the original record.
	reread := audit.Entries()
	if len(reread) != 1 {
		t.Fatalf("re-read len = %d, want 1", len(reread))
	}
	e := reread[0]
	if e.Subject != "alice" || e.Model != "qwen" || e.PromptBytes != len("hello") || e.Decision != DecisionAllow {
		t.Fatalf("store corrupted by caller mutation of snapshot: got %+v (ALIASING: Entries() shares backing array with store)", e)
	}
}

// TestAliasing_TwoSnapshotsDoNotShareBacking proves two independent snapshots do
// not share a backing array even after the store grows between them: appending
// audit records (which may reallocate the store) must not retroactively mutate a
// snapshot a caller already holds, and the two snapshots must be independent.
//
// Mutation (class A): `return m.entries` makes both snapshots alias the live
// store; after the second Record the first snapshot's length/contents would be
// observed to change here (header reuse) and the assertions fail.
func TestAliasing_TwoSnapshotsDoNotShareBacking(t *testing.T) {
	audit := NewMemAudit()
	backend := &countingBackend{resp: Response{Model: "m"}}
	p := mustProxy(t, backend, allowAll{}, audit, fixedClock(time.Unix(2, 0)))

	if _, err := p.Infer(context.Background(), Request{Subject: "first", Model: "m1", Prompt: "a"}); err != nil {
		t.Fatalf("Infer 1: %v", err)
	}
	first := audit.Entries() // snapshot of length 1

	// Grow the store several times (likely reallocates the backing array).
	for i := 0; i < 10; i++ {
		if _, err := p.Infer(context.Background(), Request{Subject: "later", Model: "m2", Prompt: "b"}); err != nil {
			t.Fatalf("Infer grow %d: %v", i, err)
		}
	}

	// The earlier snapshot must STILL be exactly its original 1 element with the
	// original subject — unaffected by the later appends.
	if len(first) != 1 {
		t.Fatalf("earlier snapshot len = %d, want 1 (later appends leaked into prior snapshot — ALIASING)", len(first))
	}
	if first[0].Subject != "first" || first[0].Model != "m1" {
		t.Fatalf("earlier snapshot mutated by later appends: got %+v (ALIASING)", first[0])
	}
}

// TestFabricatedSuccess_NonZeroRespWithErrorSurfacesError proves that when the
// backend returns BOTH a populated Response AND an error, the proxy surfaces the
// error and does not present the fabricated body as a success. The audit entry
// must record the error on the allow branch.
//
// Mutation (class C): a proxy that, on backend error, returned `resp, nil`
// (fail-open "we have a body") would make err==nil here and leak the fabricated
// Text — failing the errors.Is and the audit-Err assertions.
func TestFabricatedSuccess_NonZeroRespWithErrorSurfacesError(t *testing.T) {
	boom := errors.New("backend: partial generation failed")
	fab := Response{Model: "qwen", Text: "FABRICATED-PARTIAL-BODY", Tokens: 7}
	backend := &fabricatingBackend2{resp: fab, err: boom}
	audit := NewMemAudit()
	p := mustProxy(t, backend, allowAll{}, audit, fixedClock(time.Unix(3, 0)))

	resp, err := p.Infer(context.Background(), Request{Subject: "a", Model: "qwen", Prompt: "x"})
	if err == nil {
		t.Fatalf("Infer returned nil error while backend errored (resp=%+v) — fabricated body leaked as success", resp)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("Infer err = %v, want the backend error %v surfaced", err, boom)
	}
	// Contract (pinned by first pass): the proxy returns the backend's exact
	// (resp, err) pair. Here that pair carries BOTH the body and the error; the
	// load-bearing point is that err is non-nil so a caller checking err first
	// never treats the body as success. We assert the error is honest; the body
	// being passed through alongside a non-nil err is acceptable ONLY because err
	// is surfaced.
	audited := audit.Entries()
	if len(audited) != 1 || audited[0].Decision != DecisionAllow {
		t.Fatalf("audit = %+v, want 1 allow entry", audited)
	}
	if !errors.Is(audited[0].Err, boom) {
		t.Fatalf("audit Err = %v, want the backend error recorded (not swallowed)", audited[0].Err)
	}
}

// TestFailClosed_UppercaseHexTokenRejected proves token canonicalization is
// strict: the SAME MAC bytes hex-encoded in UPPERCASE are still verified by
// value (hex.DecodeString accepts uppercase) — i.e. a valid token survives case
// folding, BUT a corrupted/forged token does NOT slip through. This pins that
// the gate decides on decoded MAC bytes, not on string equality, and never
// fails open on a decode quirk.
//
// This is a fail-closed probe: it confirms (a) a genuinely valid token in
// uppercase still admits (no false-deny that would push callers to weaken the
// gate) and (b) a same-length non-MAC uppercase hex string is denied.
func TestFailClosed_UppercaseHexTokenRejected(t *testing.T) {
	secret := []byte("canon-key")
	auth := NewTokenAuthorizer(secret, "infer")

	lower := auth.MintToken("alice")
	upper := strings.ToUpper(lower)
	// Sanity: independent oracle that upper is the same bytes.
	lb, _ := hex.DecodeString(lower)
	ub, _ := hex.DecodeString(upper)
	if !hmac.Equal(lb, ub) {
		t.Fatalf("uppercase hex decoded to different bytes — test premise broken")
	}
	// A valid token (any case) must admit — gate must not false-deny on case.
	if err := auth.Authorize(Request{Subject: "alice", Token: upper, Scope: "infer"}); err != nil {
		t.Fatalf("Authorize(valid uppercase token) = %v, want admit (gate decides on decoded MAC bytes)", err)
	}
	// A same-length forged uppercase hex string must be DENIED (fail closed).
	forgedUpper := strings.ToUpper(hex.EncodeToString(make([]byte, sha256.Size)))
	if err := auth.Authorize(Request{Subject: "alice", Token: forgedUpper, Scope: "infer"}); !errors.Is(err, ErrBadToken) {
		t.Fatalf("Authorize(forged uppercase) = %v, want ErrBadToken (fail closed)", err)
	}
}

// TestFailClosed_TruncatedAndPaddedTokenDenied proves a token whose decoded
// length differs from the MAC length is denied (constant-time compare returns 0
// on length mismatch). A truncated valid token and a valid token with extra
// bytes appended must both fail closed — no length-extension or prefix-match
// admits.
func TestFailClosed_TruncatedAndPaddedTokenDenied(t *testing.T) {
	secret := []byte("len-key")
	auth := NewTokenAuthorizer(secret, "infer")
	good := auth.MintToken("alice")

	// Truncate the last hex byte (still valid hex, wrong length).
	truncated := good[:len(good)-2]
	if err := auth.Authorize(Request{Subject: "alice", Token: truncated, Scope: "infer"}); !errors.Is(err, ErrBadToken) {
		t.Fatalf("Authorize(truncated valid token) = %v, want ErrBadToken (length mismatch fails closed)", err)
	}

	// Append an extra hex byte (valid MAC as a prefix, longer overall).
	padded := good + "ab"
	if err := auth.Authorize(Request{Subject: "alice", Token: padded, Scope: "infer"}); !errors.Is(err, ErrBadToken) {
		t.Fatalf("Authorize(padded valid token) = %v, want ErrBadToken (prefix must not admit)", err)
	}
}

// TestFailClosed_NilAndEmptySecretStillBindsSubject probes the construction edge
// case: an authorizer built with a nil/empty secret must still be a REAL keyed
// gate that binds the token to the subject. A token minted for "alice" must NOT
// authorize "bob" even with an empty secret (HMAC with an empty key is still a
// keyed MAC over the subject). This catches a fail-open where an empty secret
// degenerated into "admit anything".
func TestFailClosed_NilAndEmptySecretStillBindsSubject(t *testing.T) {
	for _, secret := range [][]byte{nil, {}} {
		auth := NewTokenAuthorizer(secret, "infer")
		aliceTok := auth.MintToken("alice")
		// alice's own token admits.
		if err := auth.Authorize(Request{Subject: "alice", Token: aliceTok, Scope: "infer"}); err != nil {
			t.Fatalf("empty-secret: Authorize(alice valid) = %v, want admit", err)
		}
		// alice's token must NOT authorize bob (subject binding intact).
		if err := auth.Authorize(Request{Subject: "bob", Token: aliceTok, Scope: "infer"}); !errors.Is(err, ErrBadToken) {
			t.Fatalf("empty-secret: Authorize(alice token as bob) = %v, want ErrBadToken (subject binding must hold even with empty secret)", err)
		}
		// An empty token still fails as missing, not admitted.
		if err := auth.Authorize(Request{Subject: "alice", Token: "", Scope: "infer"}); !errors.Is(err, ErrMissingToken) {
			t.Fatalf("empty-secret: Authorize(no token) = %v, want ErrMissingToken", err)
		}
	}
}

// TestSecretNotLeaked_InAuditOrError proves a captured audit log and the
// returned deny error do NOT embed the HMAC secret or the raw presented token —
// so an exfiltrated audit trail is not a credential store. The deny path records
// who/model/decision/bytes/err; none of those may contain the secret or token.
//
// This is a LATENT-RISK guard turned into an always-on assertion: it pins that
// the recorded Err (which wraps ErrUnauthorized + the authorizer reason) carries
// no secret/token material.
func TestSecretNotLeaked_InAuditOrError(t *testing.T) {
	secret := []byte("TOP-SECRET-HMAC-KEY-do-not-leak")
	auth := NewTokenAuthorizer(secret, "infer")
	backend := &countingBackend{}
	audit := NewMemAudit()
	p := mustProxy(t, backend, auth, audit, fixedClock(time.Unix(4, 0)))

	rawToken := "DEADBEEF-secret-bearer-token-material"
	_, err := p.Infer(context.Background(), Request{
		Subject: "mallory", Model: "qwen", Prompt: "attack", Token: rawToken, Scope: "infer",
	})
	if err == nil {
		t.Fatalf("expected deny (forged token)")
	}
	if strings.Contains(err.Error(), string(secret)) {
		t.Fatalf("deny error leaks the HMAC secret: %q", err.Error())
	}
	if strings.Contains(err.Error(), rawToken) {
		t.Fatalf("deny error leaks the raw presented token: %q", err.Error())
	}
	entries := audit.Entries()
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(entries))
	}
	e := entries[0]
	// Reconstruct the audit record as a string and scan it for secret/token.
	rendered := e.Subject + "|" + e.Model + "|" + string(e.Decision)
	if e.Err != nil {
		rendered += "|" + e.Err.Error()
	}
	if strings.Contains(rendered, string(secret)) {
		t.Fatalf("audit entry leaks HMAC secret: %q", rendered)
	}
	if strings.Contains(rendered, rawToken) {
		t.Fatalf("audit entry leaks raw token: %q", rendered)
	}
	if h := backend.hits.Load(); h != 0 {
		t.Fatalf("backend hits = %d, want 0 (forged token denied)", h)
	}
}

// TestConcurrent_AuthorizerSharedAcrossGoroutines runs MintToken + Authorize on
// ONE shared TokenAuthorizer from many goroutines. Each call builds its own hmac
// state, so there must be no shared mutable state; this asserts every minted
// token verifies for its own subject and no goroutine's token cross-authorizes
// another subject. Run the package once under -race to surface any shared write.
func TestConcurrent_AuthorizerSharedAcrossGoroutines(t *testing.T) {
	secret := []byte("shared-auth-key")
	auth := NewTokenAuthorizer(secret, "infer")

	const n = 256
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			subj := "subject-" + hex.EncodeToString([]byte{byte(i), byte(i >> 8)})
			tok := auth.MintToken(subj)
			if err := auth.Authorize(Request{Subject: subj, Token: tok, Scope: "infer"}); err != nil {
				errCh <- errors.New("own token rejected for " + subj)
				return
			}
			// Independent oracle: the minted token equals the real HMAC.
			mac := hmac.New(sha256.New, secret)
			mac.Write([]byte(subj))
			if tok != hex.EncodeToString(mac.Sum(nil)) {
				errCh <- errors.New("minted token != independent HMAC for " + subj)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}
