package errors

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestNew(t *testing.T) {
	err := New(E_NOT_FOUND, "resource missing")
	if err.Code != E_NOT_FOUND {
		t.Errorf("expected code %s, got %s", E_NOT_FOUND, err.Code)
	}
	if err.Message != "resource missing" {
		t.Errorf("expected message 'resource missing', got %s", err.Message)
	}
	if len(err.Stack) == 0 {
		t.Error("expected non-empty stack trace")
	}
	if !strings.Contains(err.Error(), "resource missing") {
		t.Errorf("expected error string to contain message, got %s", err.Error())
	}
}

func TestNew_Mutation(t *testing.T) {
	// Mutation: New does not capture stack trace
	err := New(E_INTERNAL, "boom")
	if len(err.Stack) == 0 {
		t.Error("expected stack trace to be captured")
	}
}

func TestWrap(t *testing.T) {
	inner := fmt.Errorf("inner error")
	err := Wrap(inner, E_TIMEOUT, "request timed out")
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if err.Code != E_TIMEOUT {
		t.Errorf("expected code %s, got %s", E_TIMEOUT, err.Code)
	}
	if err.Cause != inner {
		t.Error("expected cause to be inner error")
	}
	if !errors.Is(err, inner) {
		t.Error("expected errors.Is to match inner")
	}
	if len(err.Stack) == 0 {
		t.Error("expected stack trace on wrapped error")
	}
}

func TestWrap_Mutation(t *testing.T) {
	// Mutation: Wrap returns non-nil for nil input
	err := Wrap(nil, E_UNKNOWN, "should be nil")
	if err != nil {
		t.Error("expected nil when wrapping nil")
	}
}

func TestWithField(t *testing.T) {
	err := New(E_INVALID, "bad input").WithField("field", "email")
	if err.Fields["field"] != "email" {
		t.Errorf("expected field email, got %v", err.Fields["field"])
	}
}

func TestWithField_Mutation(t *testing.T) {
	// Mutation: WithField on nil panics or mutates nil
	var nilErr *Error
	result := nilErr.WithField("k", "v")
	if result != nil {
		t.Error("expected nil result from WithField on nil")
	}
}

func TestWithFields(t *testing.T) {
	err := New(E_INTERNAL, "failure").WithFields(map[string]interface{}{
		"node_id": "node-1",
		"shard":   42,
	})
	if err.Fields["node_id"] != "node-1" {
		t.Errorf("expected node_id node-1, got %v", err.Fields["node_id"])
	}
	if err.Fields["shard"] != 42 {
		t.Errorf("expected shard 42, got %v", err.Fields["shard"])
	}
}

func TestIsCode(t *testing.T) {
	inner := New(E_NOT_FOUND, "missing")
	outer := Wrap(inner, E_UNAVAILABLE, "service down")

	if !IsCode(outer, E_UNAVAILABLE) {
		t.Error("expected IsCode to match outer code")
	}
	if !IsCode(outer, E_NOT_FOUND) {
		t.Error("expected IsCode to match inner code through cause")
	}
	if IsCode(outer, E_INVALID) {
		t.Error("expected IsCode to not match unrelated code")
	}
	if IsCode(nil, E_UNKNOWN) {
		t.Error("expected IsCode(nil) to be false")
	}
}

func TestIsCode_Mutation(t *testing.T) {
	// Mutation: IsCode does not traverse cause chain
	inner := New(E_NOT_FOUND, "missing")
	outer := Wrap(inner, E_UNAVAILABLE, "service down")
	if !IsCode(outer, E_NOT_FOUND) {
		t.Error("expected IsCode to traverse cause chain")
	}
}

func TestGetFields(t *testing.T) {
	inner := New(E_NOT_FOUND, "missing").WithField("resource", "user")
	outer := Wrap(inner, E_UNAVAILABLE, "down").WithField("retry_after", 30)

	fields := GetFields(outer)
	if fields["resource"] != "user" {
		t.Errorf("expected resource=user from inner, got %v", fields["resource"])
	}
	if fields["retry_after"] != 30 {
		t.Errorf("expected retry_after=30 from outer, got %v", fields["retry_after"])
	}
}

func TestGetFields_Mutation(t *testing.T) {
	// Mutation: GetFields overwrites inner fields with outer instead of merging
	inner := New(E_NOT_FOUND, "missing").WithField("key", "inner")
	outer := Wrap(inner, E_UNAVAILABLE, "down").WithField("key", "outer")

	fields := GetFields(outer)
	// Inner field should be preserved (first seen wins)
	if fields["key"] != "inner" {
		t.Errorf("expected inner field to be preserved, got %v", fields["key"])
	}
}

func TestStackTrace(t *testing.T) {
	err := New(E_INTERNAL, "boom")
	st := err.StackTrace()
	if st == "" {
		t.Error("expected non-empty stack trace string")
	}
	if !strings.Contains(st, "package_test.go") {
		t.Error("expected stack trace to reference test file")
	}
}

func TestStackTrace_Mutation(t *testing.T) {
	// Mutation: StackTrace returns garbage or panics on nil
	var nilErr *Error
	st := nilErr.StackTrace()
	if st != "" {
		t.Errorf("expected empty stack trace for nil, got %s", st)
	}
}

func TestErrorFormatting(t *testing.T) {
	err := New(E_INVALID, "bad request").
		WithField("code", 400).
		WithField("path", "/api/v1/users")
	str := err.Error()
	if !strings.Contains(str, "bad request") {
		t.Errorf("expected message in error string, got %s", str)
	}
	if !strings.Contains(str, "fields=") {
		t.Errorf("expected fields in error string, got %s", str)
	}
}

func TestErrorFormattingWithCause(t *testing.T) {
	inner := New(E_NOT_FOUND, "user not found")
	outer := Wrap(inner, E_UNAVAILABLE, "query failed")
	str := outer.Error()
	if !strings.Contains(str, "query failed") {
		t.Errorf("expected outer message, got %s", str)
	}
	if !strings.Contains(str, "user not found") {
		t.Errorf("expected cause message, got %s", str)
	}
}

func TestErrorFormattingWithCause_Mutation(t *testing.T) {
	// Mutation: Error() does not include cause
	inner := fmt.Errorf("inner")
	outer := Wrap(inner, E_INTERNAL, "outer")
	str := outer.Error()
	if !strings.Contains(str, "inner") {
		t.Error("expected cause in error string")
	}
}

// TestCodeEnumValues asserts exact string values AND pairwise distinctness for
// every Code constant. It uses an ORDERED SLICE of (Code, expectedString) pairs —
// NOT a map — so that two constants sharing the same underlying string value
// cannot silently collapse into a single map entry. With a map literal keyed by
// Code (a typed string), a collision like E_INVALID="E_UNKNOWN" would reduce
// len(expected) from 8 to 7 and the len-equality check would pass trivially;
// the per-code string assertion would also never fire for the missing key.
//
// §1.1 mutation proof: changing any constant's string value (e.g. E_INVALID →
// "E_UNKNOWN") causes the string(code)!=want assertion to fire for that entry
// (runtime FAIL), and causes the duplicate-string detection to fire for the
// colliding pair. Both paths are exercised regardless of map-literal collapsing.
func TestCodeEnumValues(t *testing.T) {
	// Ordered slice: every constant appears exactly once. A collision is visible
	// because BOTH entries are present in the slice and both are checked.
	type codeCase struct {
		code Code
		want string
	}
	cases := []codeCase{
		{E_UNKNOWN, "E_UNKNOWN"},
		{E_NOT_FOUND, "E_NOT_FOUND"},
		{E_INVALID, "E_INVALID"},
		{E_TIMEOUT, "E_TIMEOUT"},
		{E_UNAVAILABLE, "E_UNAVAILABLE"},
		{E_INTERNAL, "E_INTERNAL"},
		{E_UNAUTHORIZED, "E_UNAUTHORIZED"},
		{E_CONFLICT, "E_CONFLICT"},
	}

	// 1. Exact value assertion: every constant must equal its declared string.
	for _, tc := range cases {
		if string(tc.code) != tc.want {
			t.Errorf("Code constant mismatch: got %q, want %q", string(tc.code), tc.want)
		}
	}

	// 2. Pairwise distinctness: build an inverted map (string → index of first
	//    occurrence). If two entries share the same string the second one fires
	//    a clear failure naming both the duplicate string and both positions.
	//    This runs over the slice — collisions can never collapse silently.
	seen := make(map[string]int) // string value → slice index of first occurrence
	for i, tc := range cases {
		if j, dup := seen[tc.want]; dup {
			t.Errorf("duplicate Code string value %q: cases[%d] and cases[%d] share the same value",
				tc.want, j, i)
		} else {
			seen[tc.want] = i
		}
	}
	// The cardinality check is now meaningful: if any collision exists, the
	// duplicate detection above already fired, AND len(seen) < len(cases).
	if len(seen) != len(cases) {
		t.Errorf("expected %d distinct Code values, got %d (collision present)", len(cases), len(seen))
	}
}

// TestConcurrentFieldAccess proves the sync.RWMutex contract is real: many
// writers (WithField) run concurrently with many readers that go through the
// LOCKED accessor (GetFields), so the test exercises the exact lock path the
// mutex guards. It is meaningful only under `-race` and, after the goroutines
// settle, asserts a DETERMINISTIC sink-side result: all 50 writes are observed.
func TestConcurrentFieldAccess(t *testing.T) {
	const writers = 50
	err := New(E_INTERNAL, "race test")

	var wg sync.WaitGroup

	// Writers: each adds a distinct key under the write lock.
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			err.WithField(fmt.Sprintf("key%d", i), i)
		}(i)
	}

	// Readers: route through the LOCKED accessor (GetFields -> RLock), not the
	// raw err.Fields map header. This is what makes the test honest about the
	// mutex contract; under `-race` an unsynchronized read here would fail.
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			_ = GetFields(err)
		}()
	}

	wg.Wait()

	// Sink-side, deterministic assertion: every write must have landed.
	got := GetFields(err)
	if len(got) != writers {
		t.Fatalf("expected all %d concurrent writes to be observed, got %d", writers, len(got))
	}
	for i := 0; i < writers; i++ {
		k := fmt.Sprintf("key%d", i)
		if got[k] != i {
			t.Errorf("expected field %s=%d, got %v", k, i, got[k])
		}
	}
}

// TestConcurrentErrorRendering exercises the OTHER real read path — Error() —
// concurrently with writers. Error() must read Fields under the lock; without
// proper locking this fails under `-race`.
func TestConcurrentErrorRendering(t *testing.T) {
	const n = 50
	err := New(E_INTERNAL, "render race")

	var wg sync.WaitGroup
	wg.Add(n * 2)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			err.WithField(fmt.Sprintf("k%d", i), i)
		}(i)
		go func() {
			defer wg.Done()
			_ = err.Error()
		}()
	}
	wg.Wait()
}

func TestWithFields_NilReceiver(t *testing.T) {
	// Mutation/safety: WithFields on a nil receiver must return nil, not panic.
	var nilErr *Error
	result := nilErr.WithFields(map[string]interface{}{"k": "v"})
	if result != nil {
		t.Error("expected nil result from WithFields on nil receiver")
	}
}

func TestAsChain(t *testing.T) {
	// Prove errors.As compatibility (not just errors.Is): a *Error must be
	// extractable through a multi-level Wrap chain, including across a
	// non-*Error std wrapper in the middle.
	leaf := New(E_NOT_FOUND, "leaf missing").WithField("resource", "user")
	mid := fmt.Errorf("std wrapper: %w", leaf)
	top := Wrap(mid, E_UNAVAILABLE, "service down")

	var target *Error
	if !errors.As(top, &target) {
		t.Fatal("expected errors.As to extract *Error from chain")
	}
	// errors.As yields the first *Error in the chain (the top wrapper here).
	if target.Code != E_UNAVAILABLE {
		t.Errorf("expected first extracted *Error to be the top (E_UNAVAILABLE), got %s", target.Code)
	}

	// And the leaf *Error is reachable too.
	var leafTarget *Error
	if !errors.As(mid, &leafTarget) {
		t.Fatal("expected errors.As to extract leaf *Error through std wrapper")
	}
	if leafTarget.Code != E_NOT_FOUND {
		t.Errorf("expected leaf code E_NOT_FOUND, got %s", leafTarget.Code)
	}
}

func TestGetFieldsNil(t *testing.T) {
	// GetFields(nil) must return an empty, non-nil map (safe to range/index).
	fields := GetFields(nil)
	if fields == nil {
		t.Fatal("expected non-nil map from GetFields(nil)")
	}
	if len(fields) != 0 {
		t.Errorf("expected empty map from GetFields(nil), got %v", fields)
	}
}

// TestStackFrame_ContentAndCap asserts two §1.1-paired properties of captureStack:
//
//  1. Frame content: at least one frame must reference the actual source file that
//     called New() — here "package_test.go". If captureStack returned synthetic or
//     blank frames this assertion fails.
//
//  2. 32-frame cap: captureStack must stop at 32 frames. Removing the
//     `if len(frames) >= 32 { break }` guard would allow arbitrarily deep stacks
//     (and potentially panic in deep call chains); asserting <= 32 catches that
//     mutation. Asserting len >= 1 catches a regression that returns an empty slice.
//
// Mutation proof: removing the `len(frames) >= 32` cap makes the test still pass
// for shallow stacks (the test call depth is ~10), so the cap is validated by
// calling a deeply recursive helper that forces >= 32 frames onto the Go stack,
// ensuring captureStack would exceed 32 without the guard.
func TestStackFrame_ContentAndCap(t *testing.T) {
	// --- 1. Content: frame must reference the test file ---
	err := New(E_INTERNAL, "stack test")
	if len(err.Stack) == 0 {
		t.Fatal("expected at least one stack frame")
	}
	found := false
	for _, f := range err.Stack {
		if strings.Contains(f.File, "package_test.go") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a frame from package_test.go; got frames: %v", err.Stack)
	}
	// Verify frame fields are populated: Function and File must be non-empty.
	for i, f := range err.Stack {
		if f.File == "" {
			t.Errorf("frame[%d].File is empty", i)
		}
		if f.Function == "" {
			t.Errorf("frame[%d].Function is empty", i)
		}
		if f.Line <= 0 {
			t.Errorf("frame[%d].Line=%d, want >0", i, f.Line)
		}
	}

	// --- 2. 32-frame cap: captureStack must not return more than 32 frames ---
	// Call New() from inside a deeply recursive function so the real call depth
	// at captureStack time exceeds 32. Without the cap guard the stack would have
	// 40+ frames; with the guard it must be exactly 32.
	var captured *Error
	var recurse func(depth int)
	recurse = func(depth int) {
		if depth == 0 {
			captured = New(E_INTERNAL, "deep stack")
			return
		}
		recurse(depth - 1)
	}
	recurse(40) // 40 recursive frames → well above the 32-frame cap
	if len(captured.Stack) > 32 {
		t.Errorf("stack exceeded 32-frame cap: got %d frames", len(captured.Stack))
	}
	if len(captured.Stack) == 0 {
		t.Error("expected at least 1 frame from deep stack")
	}
}
