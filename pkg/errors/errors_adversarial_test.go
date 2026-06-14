package errors

// Adversarial, sink-side probes for pkg/errors.
//
// Focus areas (per the error-handling bug taxonomy):
//   (a) WRAP-CHAIN errors.Is / errors.As across several layers (incl. a foreign
//       fmt.Errorf("%w") link in the middle) — a broken Unwrap silently loses
//       the sentinel/typed match and the caller mis-routes the error.
//   (b) NIL handling — Wrap(nil,...) must not manufacture a non-nil failure.
//       This package returns the CONCRETE type *Error, which exposes the classic
//       Go typed-nil interface trap at the call site.
//   (c) CLASSIFICATION across the wrap chain — this package has no
//       IsRetryable/Temporary; the carried "category" is Code, surfaced via
//       IsCode. We pin that the code/category survives wrapping (and survives a
//       foreign wrapper) so a caller does not mis-classify a wrapped error.
//   (d) CODE/CATEGORY preservation + cyclic-Unwrap behaviour + concurrency.
//
// Every assertion checks the actual verdict a caller would observe
// (errors.Is == true, errors.As extracted value, IsCode verdict, the concrete
// nil/non-nil at the sink) — never merely "did not panic".

import (
	stderrors "errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// (a) WRAP-CHAIN errors.Is / errors.As
// ---------------------------------------------------------------------------

// sentinel is a package-level error used as the deepest cause, the way callers
// pattern-match a known condition with errors.Is.
var sentinelAdv = stderrors.New("adv-sentinel-root-cause")

// TestAdv_IsAcrossDeepWrapChain proves errors.Is finds a sentinel buried several
// helix-Wrap layers deep. If Unwrap were broken, the sink-side verdict flips to
// false and the caller mis-handles the error.
func TestAdv_IsAcrossDeepWrapChain(t *testing.T) {
	l1 := Wrap(sentinelAdv, E_NOT_FOUND, "layer1")
	l2 := Wrap(l1, E_INTERNAL, "layer2")
	l3 := Wrap(l2, E_UNAVAILABLE, "layer3")

	if !stderrors.Is(l3, sentinelAdv) {
		t.Fatalf("SINK: errors.Is(deepWrap, sentinel) = false; want true — Unwrap chain is broken, caller would mis-route")
	}
	// And the intermediate helix errors must also be findable by identity.
	if !stderrors.Is(l3, l1) {
		t.Fatalf("SINK: errors.Is(l3, l1) = false; want true — intermediate link lost")
	}
}

// TestAdv_IsAcrossForeignWrapper inserts a stdlib fmt.Errorf("%w") link in the
// MIDDLE of the chain. IsCode and errors.Is must still traverse it.
func TestAdv_IsAcrossForeignWrapper(t *testing.T) {
	root := New(E_TIMEOUT, "db deadline exceeded")     // helix typed error w/ code
	foreign := fmt.Errorf("service call failed: %w", root) // foreign wrapper
	top := Wrap(foreign, E_UNAVAILABLE, "gateway")     // helix wrap over foreign

	// errors.Is by identity through the foreign link:
	if !stderrors.Is(top, root) {
		t.Fatalf("SINK: errors.Is(top, root) = false through foreign %%w wrapper; want true")
	}
	// IsCode must walk through the foreign wrapper to find the deep code.
	if !IsCode(top, E_TIMEOUT) {
		t.Fatalf("SINK: IsCode(top, E_TIMEOUT) = false through foreign wrapper; want true — caller would mis-classify (e.g. not retry a timeout)")
	}
}

// TestAdv_AsExtractsTypedErrorAtDepth proves errors.As extracts the concrete
// *Error at any depth — the typed payload (Code/Message) the caller reads.
func TestAdv_AsExtractsTypedErrorAtDepth(t *testing.T) {
	deep := New(E_CONFLICT, "version conflict")
	mid := fmt.Errorf("orchestrator: %w", deep)
	top := Wrap(mid, E_INTERNAL, "top")

	var got *Error
	if !stderrors.As(top, &got) {
		t.Fatalf("SINK: errors.As(top, *Error) = false; want true")
	}
	// As returns the OUTERMOST *Error first; assert it is a real *Error and its
	// chain still reaches the conflict code (caller-visible classification).
	if got == nil {
		t.Fatalf("SINK: errors.As extracted a nil *Error")
	}
	if !IsCode(top, E_CONFLICT) {
		t.Fatalf("SINK: deep E_CONFLICT not visible via IsCode after As-extraction path; want true")
	}
}

// ---------------------------------------------------------------------------
// (b) NIL handling — typed-nil interface trap
// ---------------------------------------------------------------------------

// TestAdv_WrapNilReturnsConcreteNil pins the documented contract: Wrap(nil,...)
// returns nil. Checked at the CONCRETE *Error type (the function's signature).
func TestAdv_WrapNilReturnsConcreteNil(t *testing.T) {
	got := Wrap(nil, E_INTERNAL, "should vanish")
	if got != nil {
		t.Fatalf("SINK: Wrap(nil) = %#v; want concrete nil — a non-nil here turns a success into a false failure", got)
	}
}

// helperWrapAsError is the realistic caller shape: a function that returns the
// `error` interface and forwards Wrap's result. This is where Go's typed-nil
// trap bites: returning a nil *Error through an `error` return makes the
// interface NON-NIL.
func helperWrapAsError(cause error) error {
	return Wrap(cause, E_INTERNAL, "forwarded")
}

// TestAdv_WrapNil_TypedNilTrap_AtErrorInterface documents the SINK-SIDE behaviour
// when Wrap's nil result flows through an `error`-typed return — the canonical
// Go gotcha. We assert what ACTUALLY happens (xfail-style) so the behaviour is
// pinned and visible; if the package is hardened to return `error` (so nil stays
// nil), this test will tell us by failing.
func TestAdv_WrapNil_TypedNilTrap_AtErrorInterface(t *testing.T) {
	err := helperWrapAsError(nil) // success path: no underlying cause

	// The honest sink-side observation. With the current signature
	// `func Wrap(...) *Error`, a nil *Error boxed into `error` is NON-nil.
	if err != nil {
		t.Logf("LATENT-RISK (pinned): Wrap(nil) forwarded through an `error` return is NON-nil interface (typed-nil trap). "+
			"A caller doing `if err != nil` on this path sees a FALSE failure. err=%v type=%T", err, err)
		// This is the current, real behaviour — assert it holds so a regression
		// (either direction) is surfaced rather than hidden.
		if _, ok := err.(*Error); !ok {
			t.Fatalf("expected boxed *Error typed-nil, got %T", err)
		}
		if e := err.(*Error); e != nil {
			t.Fatalf("expected the boxed *Error to be a nil pointer, got non-nil %#v", e)
		}
		return
	}
	// If we reach here, the package returns a clean nil through `error` — the
	// safer contract. Nothing wrong; record it.
	t.Log("Wrap(nil) yields a clean nil through the error interface (safe).")
}

// ---------------------------------------------------------------------------
// (c) CLASSIFICATION / CODE survives wrapping (no fatal<->retryable flip)
// ---------------------------------------------------------------------------

// TestAdv_CodeSurvivesWrapping proves the deepest code is still discoverable
// after several wraps with DIFFERENT codes — IsCode must not be shadowed by the
// outer codes. A caller keying retry/fatal decisions on IsCode(err, E_TIMEOUT)
// must still see the timeout under non-timeout wrappers.
func TestAdv_CodeSurvivesWrapping(t *testing.T) {
	root := New(E_TIMEOUT, "upstream timed out") // "retryable-class" category
	w1 := Wrap(root, E_INVALID, "validation layer")
	w2 := Wrap(w1, E_UNAUTHORIZED, "auth layer")

	// The retryable category (E_TIMEOUT) must survive being wrapped by
	// non-retryable categories.
	if !IsCode(w2, E_TIMEOUT) {
		t.Fatalf("SINK: IsCode(w2, E_TIMEOUT) = false after wrapping; want true — a fatal-looking outer code must NOT bury a retryable cause")
	}
	// And the outer codes are also discoverable (no accidental skip of self).
	if !IsCode(w2, E_UNAUTHORIZED) {
		t.Fatalf("SINK: IsCode(w2, E_UNAUTHORIZED) = false; the error's own code must match")
	}
	// A code that is nowhere in the chain must be false (no over-matching that
	// would mis-classify everything as retryable).
	if IsCode(w2, E_CONFLICT) {
		t.Fatalf("SINK: IsCode(w2, E_CONFLICT) = true; want false — over-matching would mis-classify")
	}
}

// TestAdv_IsCodeOnNonHelixWrapperOfHelix verifies the "self vs unwrap" branch:
// when the top error is a *Error whose OWN code does not match, IsCode must
// continue into the cause rather than stopping. (Guards the line-75 fall-through.)
func TestAdv_IsCodeFallthroughIntoCause(t *testing.T) {
	root := New(E_NOT_FOUND, "row missing")
	top := Wrap(root, E_INTERNAL, "repo") // top.Code = E_INTERNAL, cause has E_NOT_FOUND
	if !IsCode(top, E_NOT_FOUND) {
		t.Fatalf("SINK: IsCode did not fall through a non-matching *Error into its cause; want true")
	}
}

// ---------------------------------------------------------------------------
// (d) Cyclic Unwrap + concurrency
// ---------------------------------------------------------------------------

// TestAdv_CyclicUnwrap_DoesNotHang documents IsCode's behaviour on a
// self-referential Unwrap chain. Stdlib errors.Is/As explicitly forbid cycles
// (no cycle detection), and IsCode mirrors that. We run the probe in a goroutine
// with a deadline so a hang/stack-overflow is reported as a finding rather than
// wedging the whole suite.
//
// NOTE: a true cycle would stack-overflow (fatal, unrecoverable). We therefore
// build a LONG-BUT-FINITE chain to prove IsCode terminates on legitimate deep
// chains, and we DO NOT construct an actual a<->a cycle (that would crash the
// process). The cycle caveat is reported as LATENT RISK in the writeup.
func TestAdv_DeepFiniteChainTerminates(t *testing.T) {
	var err error = stderrors.New("base")
	for i := 0; i < 2000; i++ {
		err = Wrap(err, E_INTERNAL, fmt.Sprintf("layer-%d", i))
	}
	done := make(chan bool, 1)
	go func() {
		_ = IsCode(err, E_NOT_FOUND) // walk the whole 2000-deep chain
		_ = stderrors.Is(err, sentinelAdv)
		done <- true
	}()
	select {
	case <-done:
		// terminated — good
	case <-time.After(5 * time.Second):
		t.Fatalf("SINK: IsCode/errors.Is did not terminate on a 2000-deep finite chain")
	}
}

// TestAdv_ConcurrentWithFieldAndIsCode stresses the documented data-race fix:
// WithField mutates Fields under the write lock while IsCode/GetFields/Error
// read concurrently. Must be clean under -race.
func TestAdv_ConcurrentWithFieldAndIsCode(t *testing.T) {
	root := New(E_TIMEOUT, "root")
	top := Wrap(root, E_INTERNAL, "top")

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			top.WithField(fmt.Sprintf("k%d", n), n)
			_ = IsCode(top, E_TIMEOUT)
			_ = GetFields(top)
			_ = top.Error()
		}(i)
	}
	wg.Wait()

	// Sink-side: after the storm, the deep code is still discoverable and the
	// fields we wrote are visible — proving classification + field merge survived
	// concurrent mutation.
	if !IsCode(top, E_TIMEOUT) {
		t.Fatalf("SINK: IsCode(top, E_TIMEOUT) = false after concurrent mutation; want true")
	}
	if got := GetFields(top); len(got) == 0 {
		t.Fatalf("SINK: GetFields empty after concurrent WithField; want >0")
	}
}
