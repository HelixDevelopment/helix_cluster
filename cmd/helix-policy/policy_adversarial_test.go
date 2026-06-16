package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/internal/policy"
)

// ---------------------------------------------------------------------------
// Adversarial HTTP-layer probes for cmd/helix-policy.
//
// CONTEXT: internal/policy had a remote fail-open (non-numeric node.health
// permitted) reachable through THIS daemon's /evaluate endpoint with
// caller-supplied JSON. These tests assert SINK-SIDE — the actual HTTP status
// and the Allow verdict on crafted requests — that a malformed / empty /
// missing-input request NEVER defaults to a permit (Allowed=true / 200-allow).
//
// All probes go through the real run() server (real net.Listener, real OPA
// engine, scheduling policy pre-loaded) via the existing startServer helper in
// main_test.go.
// ---------------------------------------------------------------------------

// postEval POSTs an arbitrary body to /evaluate and returns status + decoded
// response (response only meaningful on 200).
func postEval(t *testing.T, baseURL string, body []byte) (int, evalResponse) {
	t.Helper()
	resp, err := http.Post(baseURL+"/evaluate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /evaluate: %v", err)
	}
	defer resp.Body.Close()
	var out evalResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode 200 body: %v", err)
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode, out
}

// TestAdversarial_EvaluateMalformedNodeHealthDenies is the headline fail-open
// probe. The scheduling policy is pre-loaded. We feed inputs whose node.health
// is NON-NUMERIC, MISSING, or whose node/input is entirely absent. A correct
// fail-CLOSED daemon must answer 200 with Allowed=FALSE for every one of these:
// a degraded/unknown node must never be scheduled.
//
// SINK-SIDE assertion: if any of these returns Allowed=true (or a non-200 that
// a caller might treat as permit), that is a remote fail-open.
func TestAdversarial_EvaluateMalformedNodeHealthDenies(t *testing.T) {
	baseURL, stop := startServer(t, Config{})
	defer stop()
	waitHealthy(t, baseURL)

	cases := []struct {
		name  string
		input interface{}
	}{
		{"health_string", map[string]interface{}{"node": map[string]interface{}{"health": "80"}}},
		{"health_bool_true", map[string]interface{}{"node": map[string]interface{}{"health": true}}},
		{"health_null", map[string]interface{}{"node": map[string]interface{}{"health": nil}}},
		{"health_object", map[string]interface{}{"node": map[string]interface{}{"health": map[string]interface{}{"x": 1}}}},
		{"health_missing", map[string]interface{}{"node": map[string]interface{}{"region": "eu"}}},
		{"node_missing", map[string]interface{}{"other": 1}},
		{"input_empty_obj", map[string]interface{}{}},
		{"input_null", nil},
		{"node_is_string", map[string]interface{}{"node": "not-a-map"}},
		// Classic injection of a huge numeric-looking string — must NOT be coerced.
		{"health_numeric_string_big", map[string]interface{}{"node": map[string]interface{}{"health": "999999"}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]interface{}{
				"policy": "scheduling",
				"input":  c.input,
			})
			status, out := postEval(t, baseURL, body)
			if status != http.StatusOK {
				// A non-200 here is acceptable ONLY if it is a deny-equivalent
				// (4xx). It must never be a 2xx permit. Record and continue.
				if status >= 200 && status < 300 {
					t.Fatalf("FAIL-OPEN: malformed input %q got 2xx status=%d (caller may treat as permit)", c.name, status)
				}
				return
			}
			if out.Allowed {
				t.Fatalf("FAIL-OPEN: malformed/missing node.health %q was ALLOWED (status=200, allowed=true, reason=%q)", c.name, out.Reason)
			}
		})
	}
}

// TestAdversarial_EvaluateContractStatuses pins the request-validation contract:
// decode errors, wrong method, and empty/unknown policy must NOT become permits.
func TestAdversarial_EvaluateContractStatuses(t *testing.T) {
	baseURL, stop := startServer(t, Config{})
	defer stop()
	waitHealthy(t, baseURL)

	t.Run("garbage_json_is_400_not_permit", func(t *testing.T) {
		status, out := postEval(t, baseURL, []byte("{this is not json"))
		if status != http.StatusBadRequest {
			t.Fatalf("garbage JSON status=%d want 400", status)
		}
		if out.Allowed {
			t.Fatalf("FAIL-OPEN: garbage JSON yielded Allowed=true")
		}
	})

	t.Run("empty_body_is_400", func(t *testing.T) {
		status, _ := postEval(t, baseURL, []byte(""))
		if status != http.StatusBadRequest {
			t.Fatalf("empty body status=%d want 400 (EOF decode error)", status)
		}
	})

	t.Run("empty_policy_name_not_permit", func(t *testing.T) {
		// policy:"" -> map lookup miss -> 404, NOT a 200 permit.
		body, _ := json.Marshal(map[string]interface{}{
			"policy": "",
			"input":  map[string]interface{}{"node": map[string]interface{}{"health": 80}},
		})
		status, out := postEval(t, baseURL, body)
		if status == http.StatusOK && out.Allowed {
			t.Fatalf("FAIL-OPEN: empty policy name returned 200 Allowed=true")
		}
		if status != http.StatusNotFound {
			t.Fatalf("empty policy name status=%d want 404", status)
		}
	})

	t.Run("unknown_policy_is_404", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"policy": "does-not-exist",
			"input":  map[string]interface{}{},
		})
		status, _ := postEval(t, baseURL, body)
		if status != http.StatusNotFound {
			t.Fatalf("unknown policy status=%d want 404", status)
		}
	})

	t.Run("wrong_method_GET_is_405", func(t *testing.T) {
		resp, err := http.Get(baseURL + "/evaluate")
		if err != nil {
			t.Fatalf("GET /evaluate: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("GET /evaluate status=%d want 405", resp.StatusCode)
		}
	})

	t.Run("wrong_content_type_still_decoded_or_400", func(t *testing.T) {
		// The handler ignores Content-Type and decodes the body as JSON.
		// A valid JSON body with text/plain must behave the same; a garbage
		// body must still 400 (never permit).
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/evaluate",
			strings.NewReader("not json at all"))
		req.Header.Set("Content-Type", "text/plain")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST text/plain: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var out evalResponse
			_ = json.NewDecoder(resp.Body).Decode(&out)
			if out.Allowed {
				t.Fatalf("FAIL-OPEN: text/plain garbage yielded Allowed=true")
			}
			t.Fatalf("text/plain garbage got 200, want 400")
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("text/plain garbage status=%d want 400", resp.StatusCode)
		}
	})

	t.Run("trailing_garbage_after_object", func(t *testing.T) {
		// json.Decoder.Decode reads only the first value; trailing garbage is
		// silently ignored. Document that this is decoded (not rejected) but
		// still must not flip a deny to a permit.
		body := []byte(`{"policy":"scheduling","input":{"node":{"health":"oops"}}} GARBAGE`)
		status, out := postEval(t, baseURL, body)
		if status == http.StatusOK && out.Allowed {
			t.Fatalf("FAIL-OPEN: trailing-garbage request with non-numeric health was ALLOWED")
		}
	})
}

// TestAdversarial_ConcurrentLoadAndEvaluateRace hammers the shared engine from
// capped goroutines: concurrent POST /policies (writers) and POST /evaluate
// (readers) sharing one engine. Run with -race to surface unguarded shared
// state. Decisions for the deny case must remain correct under contention.
func TestAdversarial_ConcurrentLoadAndEvaluateRace(t *testing.T) {
	baseURL, stop := startServer(t, Config{})
	defer stop()
	waitHealthy(t, baseURL)

	const workers = 8 // anti-hang: capped concurrency
	const iters = 12

	var wg sync.WaitGroup
	errCh := make(chan error, workers*iters)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			client := &http.Client{Timeout: 5 * time.Second}
			for i := 0; i < iters; i++ {
				if id%2 == 0 {
					// Writer: load a uniquely named policy.
					name := fmt.Sprintf("p-%d-%d", id, i)
					lb, _ := json.Marshal(map[string]string{
						"name": name,
						"rego": "package p\ndefault allow = false\nallow { input.ok == true }",
					})
					resp, err := client.Post(baseURL+"/policies", "application/json", bytes.NewReader(lb))
					if err != nil {
						errCh <- fmt.Errorf("load: %w", err)
						continue
					}
					resp.Body.Close()
				} else {
					// Reader: evaluate the pre-loaded scheduling policy with an
					// UNHEALTHY node — must always DENY.
					eb, _ := json.Marshal(map[string]interface{}{
						"policy": "scheduling",
						"input":  map[string]interface{}{"node": map[string]interface{}{"health": 10}},
					})
					resp, err := client.Post(baseURL+"/evaluate", "application/json", bytes.NewReader(eb))
					if err != nil {
						errCh <- fmt.Errorf("eval: %w", err)
						continue
					}
					var out evalResponse
					_ = json.NewDecoder(resp.Body).Decode(&out)
					resp.Body.Close()
					if resp.StatusCode == http.StatusOK && out.Allowed {
						errCh <- fmt.Errorf("FAIL-OPEN under concurrency: unhealthy node (health=10) ALLOWED")
					}
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

// TestAdversarial_EvaluateDeterminism proves the same request yields the same
// decision and the same rendered reason across repeated calls (no map-order or
// other nondeterminism leaking into the verdict).
func TestAdversarial_EvaluateDeterminism(t *testing.T) {
	baseURL, stop := startServer(t, Config{})
	defer stop()
	waitHealthy(t, baseURL)

	body, _ := json.Marshal(map[string]interface{}{
		"policy": "scheduling",
		"input": map[string]interface{}{
			"node": map[string]interface{}{"health": 80},
			"a":    1, "b": 2, "c": 3, "d": 4, // extra keys to perturb map order
		},
	})

	var firstAllow bool
	var firstReason string
	for i := 0; i < 25; i++ {
		status, out := postEval(t, baseURL, body)
		if status != http.StatusOK {
			t.Fatalf("iter %d status=%d want 200", i, status)
		}
		if i == 0 {
			firstAllow, firstReason = out.Allowed, out.Reason
			if !firstAllow {
				t.Fatalf("health=80 should be ALLOWED, got deny (reason=%q)", out.Reason)
			}
			continue
		}
		if out.Allowed != firstAllow {
			t.Fatalf("NONDETERMINISM: iter %d Allowed=%v, first=%v", i, out.Allowed, firstAllow)
		}
		if out.Reason != firstReason {
			t.Fatalf("NONDETERMINISM: iter %d Reason=%q, first=%q", i, out.Reason, firstReason)
		}
	}
}

// guard ensures the imported policy package is referenced even if the suite is
// trimmed; it also documents the engine seam under test.
var _ = policy.SchedulingPolicyName
