package inference

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestReferenceBackendOutputIsLabeled asserts the ReferenceBackend output is
// honestly labeled as reference (never claims to be real LLM inference) and
// carries the served backend + model + prompt.
//
// MUTATION: in referenceGenerate, drop the "[reference:..." prefix (e.g. return
// just req.Prompt). The strings.HasPrefix(..., "[reference:") check fails,
// catching a dishonest "looks like real inference" output.
func TestReferenceBackendOutputIsLabeled(t *testing.T) {
	t.Parallel()
	b := &ReferenceBackend{
		BackendName: "ref", Up: true,
		Caps: Capabilities{Models: []string{"m"}},
	}
	resp, err := b.Infer(context.Background(), Request{Model: "m", Prompt: "hello", MaxTokens: 8})
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	if !strings.HasPrefix(resp.Text, "[reference:ref") {
		t.Fatalf("resp.Text = %q, want honest [reference:...] label", resp.Text)
	}
	if !strings.Contains(resp.Text, "hello") {
		t.Fatalf("resp.Text = %q, want it to carry the prompt", resp.Text)
	}
	if resp.Tokens != 8 {
		t.Fatalf("resp.Tokens = %d, want 8 (requested budget)", resp.Tokens)
	}
}

// TestReferenceBackendUnavailableInfer asserts a down backend's Infer returns
// ErrUnavailable.
//
// MUTATION: delete the `if !b.Up { return ErrUnavailable }` guard in Infer. A
// down backend would still produce output; errors.Is(err, ErrUnavailable) fails.
func TestReferenceBackendUnavailableInfer(t *testing.T) {
	t.Parallel()
	b := &ReferenceBackend{
		BackendName: "ref", Up: false,
		Caps: Capabilities{Models: []string{"m"}},
	}
	_, err := b.Infer(context.Background(), Request{Model: "m", Prompt: "hi"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

// TestCapabilitiesSupports exercises the matching predicate directly with a
// table.
//
// MUTATION: in Supports, make supportsModel always return true. The
// "wrong model" row (want false) fails.
func TestCapabilitiesSupports(t *testing.T) {
	t.Parallel()
	caps := Capabilities{
		Models:        []string{"a", "b"},
		Architectures: []string{"llama"},
		MaxTokens:     256,
	}
	tests := []struct {
		name string
		req  Request
		want bool
	}{
		{"match model+arch+tokens", Request{Model: "a", Architecture: "llama", MaxTokens: 100}, true},
		{"wrong model", Request{Model: "z", Architecture: "llama"}, false},
		{"wrong arch", Request{Model: "a", Architecture: "qwen"}, false},
		{"over token cap", Request{Model: "a", Architecture: "llama", MaxTokens: 300}, false},
		{"empty arch ok when unconstrained on model b", Request{Model: "b", Architecture: "llama"}, true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := caps.Supports(tc.req); got != tc.want {
				t.Fatalf("Supports(%+v) = %v, want %v", tc.req, got, tc.want)
			}
		})
	}
}
