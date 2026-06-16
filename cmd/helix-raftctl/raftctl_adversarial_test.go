package main

// Adversarial sink-side probes for cmd/helix-raftctl — the raft admin CLI client.
//
// These tests are NON-e2e (no build tag): they use net/http/httptest to stand in
// for the helix-raftd admin API so the suite COMPLETES under `go test -race
// -timeout 90s` with NO real cluster. Every assertion is SINK-SIDE: it pins the
// actual process exit code (exitOK/exitError/exitUsage) and/or the actual
// stdout/stderr text produced by run(), NOT "it compiles".
//
// Classes probed (honestly mapped):
//   (a) ARG/VALIDATION BYPASS — empty/malformed --admin/--key/--value reaching an
//       unsafe op or being silently accepted. We prove the sink each takes.
//   (b) RESPONSE FAIL-OPEN — a 500 / garbage / empty server response treated as
//       success (exit 0, "OK") when it must report failure. THIS is the bug class
//       to catch; we drive httptest to return non-2xx / garbage and pin the exit
//       code + output for status/get/put/metrics.
//   (c) UNGUARDED SHARED STATE — concurrency / data race. Scoped: a single run()
//       invocation is single-shot, but the *client.hc is shared across the
//       per-node loop; we drive concurrent run() calls under -race to prove no race.
//   (d) DETERMINISM — output over maps. Scoped: run() iterates the user-ordered
//       --admin slice, no Go map ranging in any output path; we pin the order.

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// runArgs drives the exact run() seam main() uses and returns (exit, stdout, stderr).
func runArgs(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// hostport strips the scheme from an httptest server URL to the bare host:port the
// CLI's --admin flag expects (normalizeAddr re-adds http://).
func hostport(ts *httptest.Server) string {
	return strings.TrimPrefix(ts.URL, "http://")
}

// ---------------------------------------------------------------------------
// (b) RESPONSE FAIL-OPEN — the headline class. A non-2xx / garbage / empty server
// response MUST surface as a non-zero exit + a clear error, never exit 0 / "OK".
// ---------------------------------------------------------------------------

// status against a server that returns HTTP 500 must report failure (exitError),
// not silently print a zero-value status block at exit 0.
func TestStatus_Server500_ReportsFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	code, out, errOut := runArgs("status", "--admin", hostport(ts))
	if code != exitError {
		t.Fatalf("FAIL-OPEN: status on HTTP 500 returned exit=%d (want %d). stdout=%q stderr=%q", code, exitError, out, errOut)
	}
	if !strings.Contains(errOut, "HTTP 500") {
		t.Fatalf("status 500: stderr did not name HTTP 500: %q", errOut)
	}
	if strings.Contains(out, "is-leader") {
		t.Fatalf("status 500: must NOT print a status block; stdout=%q", out)
	}
	t.Logf("SINK status(500) -> exit=%d stderr=%q", code, strings.TrimSpace(errOut))
}

// status against a 200 with a GARBAGE (non-JSON) body must error, not print junk
// as success.
func TestStatus_Garbage200Body_ReportsFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("this is not json <<<>>>"))
	}))
	defer ts.Close()

	code, out, errOut := runArgs("status", "--admin", hostport(ts))
	if code != exitError {
		t.Fatalf("FAIL-OPEN: status on garbage 200 returned exit=%d (want %d). stdout=%q stderr=%q", code, exitError, out, errOut)
	}
	if !strings.Contains(errOut, "malformed JSON") {
		t.Fatalf("status garbage: stderr did not name malformed JSON: %q", errOut)
	}
	t.Logf("SINK status(garbage 200) -> exit=%d stderr=%q", code, strings.TrimSpace(errOut))
}

// status against a 200 with an EMPTY body must error (empty is not valid JSON),
// not exit 0 with a zero-value block.
func TestStatus_Empty200Body_ReportsFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // zero-length body
	}))
	defer ts.Close()

	code, _, errOut := runArgs("status", "--admin", hostport(ts))
	if code != exitError {
		t.Fatalf("FAIL-OPEN: status on empty 200 body returned exit=%d (want %d). stderr=%q", code, exitError, errOut)
	}
	t.Logf("SINK status(empty 200) -> exit=%d stderr=%q", code, strings.TrimSpace(errOut))
}

// get against a 500 must report failure, not "not found"/exit 0.
func TestGet_Server500_ReportsFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	code, out, errOut := runArgs("get", "--admin", hostport(ts), "--key", "k")
	if code != exitError {
		t.Fatalf("FAIL-OPEN: get on HTTP 500 returned exit=%d (want %d). stdout=%q stderr=%q", code, exitError, out, errOut)
	}
	if !strings.Contains(errOut, "HTTP 500") {
		t.Fatalf("get 500: stderr did not name HTTP 500: %q", errOut)
	}
	t.Logf("SINK get(500) -> exit=%d stderr=%q", code, strings.TrimSpace(errOut))
}

// get against a 200 body that decodes to found=false must report not-found with a
// NON-zero exit (so scripts can branch), not exit 0.
func TestGet_FoundFalse_NonZeroExit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"key":"k","value":"","found":false}`))
	}))
	defer ts.Close()

	code, out, errOut := runArgs("get", "--admin", hostport(ts), "--key", "k")
	if code != exitError {
		t.Fatalf("get found=false: returned exit=%d (want %d). stdout=%q stderr=%q", code, exitError, out, errOut)
	}
	if !strings.Contains(errOut, "not found") {
		t.Fatalf("get found=false: stderr lacked 'not found': %q", errOut)
	}
	t.Logf("SINK get(found=false) -> exit=%d stderr=%q", code, strings.TrimSpace(errOut))
}

// put against a server that returns 500 must report failure, not "OK ... written".
func TestPut_Server500_ReportsFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	code, out, errOut := runArgs("put", "--admin", hostport(ts), "--key", "k", "--value", "v")
	if code != exitError {
		t.Fatalf("FAIL-OPEN: put on HTTP 500 returned exit=%d (want %d). stdout=%q stderr=%q", code, exitError, out, errOut)
	}
	if strings.Contains(out, "OK:") {
		t.Fatalf("put 500: must NOT print OK; stdout=%q", out)
	}
	t.Logf("SINK put(500) -> exit=%d stderr=%q", code, strings.TrimSpace(errOut))
}

// put against a 421 follower with NO resolvable leader admin must FAIL (can't
// redirect), not falsely claim success.
func TestPut_421NoLeader_ReportsFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always a follower; /status reports isLeader:false so resolveLeaderAdmin
		// finds no leader among the supplied addresses.
		if r.URL.Path == "/status" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"n1","state":"Follower","isLeader":false,"leader":"n9","leaderAddr":"10.0.0.9:9000"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMisdirectedRequest)
		_, _ = w.Write([]byte(`{"error":"not leader","leader":"n9","leaderAddr":"10.0.0.9:9000"}`))
	}))
	defer ts.Close()

	code, out, errOut := runArgs("put", "--admin", hostport(ts), "--key", "k", "--value", "v")
	if code != exitError {
		t.Fatalf("FAIL-OPEN: put 421-no-leader returned exit=%d (want %d). stdout=%q stderr=%q", code, exitError, out, errOut)
	}
	if strings.Contains(out, "OK:") {
		t.Fatalf("put 421-no-leader: must NOT print OK; stdout=%q", out)
	}
	if !strings.Contains(errOut, "follower") {
		t.Fatalf("put 421-no-leader: stderr should explain the follower/no-leader condition: %q", errOut)
	}
	t.Logf("SINK put(421,no-leader) -> exit=%d stderr=%q", code, strings.TrimSpace(errOut))
}

// metrics against a 500 must report failure, not print an empty success.
func TestMetrics_Server500_ReportsFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	code, out, errOut := runArgs("metrics", "--admin", hostport(ts))
	if code != exitError {
		t.Fatalf("FAIL-OPEN: metrics on HTTP 500 returned exit=%d (want %d). stdout=%q stderr=%q", code, exitError, out, errOut)
	}
	if out != "" {
		t.Fatalf("metrics 500: must print nothing to stdout; got %q", out)
	}
	t.Logf("SINK metrics(500) -> exit=%d stderr=%q", code, strings.TrimSpace(errOut))
}

// CONTRACT PIN: the happy path — a well-formed put 200 reports success with exit 0.
// This anchors the positive direction so the fail-open tests above are meaningful.
func TestPut_Happy200_ReportsOK(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	code, out, errOut := runArgs("put", "--admin", hostport(ts), "--key", "k", "--value", "v")
	if code != exitOK {
		t.Fatalf("put happy: exit=%d (want %d) stderr=%q", code, exitOK, errOut)
	}
	if !strings.Contains(out, "OK: k=v written") {
		t.Fatalf("put happy: stdout lacked OK line: %q", out)
	}
	t.Logf("SINK put(200) -> exit=%d stdout=%q", code, strings.TrimSpace(out))
}

// ---------------------------------------------------------------------------
// (a) ARG / VALIDATION BYPASS — empty/malformed flags must take the documented
// sink (exitUsage with a clear message), never panic, never silently proceed.
// ---------------------------------------------------------------------------

func TestArg_MissingAdmin_Usage(t *testing.T) {
	for _, sub := range []string{"status", "get", "put", "leader", "metrics"} {
		args := []string{sub}
		if sub == "get" || sub == "put" {
			args = append(args, "--key", "k")
		}
		if sub == "put" {
			args = append(args, "--value", "v")
		}
		code, _, errOut := runArgs(args...)
		if code != exitUsage {
			t.Fatalf("%s without --admin: exit=%d (want %d) stderr=%q", sub, code, exitUsage, errOut)
		}
		if !strings.Contains(errOut, "--admin is required") {
			t.Fatalf("%s without --admin: stderr lacked required msg: %q", sub, errOut)
		}
	}
}

// An --admin made ENTIRELY of commas/whitespace splits to zero addresses and MUST
// be rejected as usage, not silently accepted (empty-target bypass).
func TestArg_AdminAllCommas_Usage(t *testing.T) {
	code, _, errOut := runArgs("status", "--admin", " , , ")
	if code != exitUsage {
		t.Fatalf("admin all-commas: exit=%d (want %d) stderr=%q", code, exitUsage, errOut)
	}
	if !strings.Contains(errOut, "--admin is required") {
		t.Fatalf("admin all-commas: stderr lacked required msg: %q", errOut)
	}
	t.Logf("SINK status(--admin ' , , ') -> exit=%d (empty-target rejected)", code)
}

// put with an empty --value must be rejected (documented: avoid a silent empty set),
// even though the daemon itself would permit it.
func TestArg_PutEmptyValue_Usage(t *testing.T) {
	code, _, errOut := runArgs("put", "--admin", "127.0.0.1:1", "--key", "k")
	if code != exitUsage {
		t.Fatalf("put empty value: exit=%d (want %d) stderr=%q", code, exitUsage, errOut)
	}
	if !strings.Contains(errOut, "--value is required") {
		t.Fatalf("put empty value: stderr lacked value-required msg: %q", errOut)
	}
}

// A malformed --admin that http.Client cannot dial (contains a space) must surface
// as a clear error + non-zero exit, NEVER a panic. We assert no panic by reaching
// the sink, and that the exit is an error/usage code (not exit 0).
func TestArg_MalformedAdmin_NoPanic(t *testing.T) {
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("PANIC on malformed --admin: %v", rec)
		}
	}()
	// Embedded space + control char => url parse / dial failure inside net/http.
	code, out, errOut := runArgs("status", "--admin", "host with space:99999")
	if code == exitOK {
		t.Fatalf("malformed admin: returned exit 0 (silently accepted). stdout=%q", out)
	}
	t.Logf("SINK status(malformed admin) -> exit=%d stderr=%q (no panic)", code, strings.TrimSpace(errOut))
}

// A key/value containing URL metacharacters (& = ? # space) must be percent-encoded
// and round-trip safely — NOT injected into the query as extra params. We inspect
// the request the server actually received (sink-side).
func TestArg_KeyValueInjection_Escaped(t *testing.T) {
	var gotRawQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		// Echo back the parsed key/value so we can assert no smuggling occurred.
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	const evilKey = "k&value=SMUGGLED&x=1"
	const evilVal = "v?#=&space here"
	code, _, errOut := runArgs("put", "--admin", hostport(ts), "--key", evilKey, "--value", evilVal)
	if code != exitOK {
		t.Fatalf("put injection: exit=%d (want %d) stderr=%q", code, exitOK, errOut)
	}
	// The server must see EXACTLY key=<escaped evilKey> and value=<escaped evilVal>;
	// the smuggled "value=SMUGGLED" must not appear as a real separate param.
	if strings.Contains(gotRawQuery, "value=SMUGGLED") {
		t.Fatalf("INJECTION: smuggled param leaked into raw query: %q", gotRawQuery)
	}
	if !strings.Contains(gotRawQuery, "key=") || !strings.Contains(gotRawQuery, "value=") {
		t.Fatalf("put injection: raw query missing key/value: %q", gotRawQuery)
	}
	t.Logf("SINK put(evil key/value) rawQuery=%q (smuggle blocked)", gotRawQuery)
}

// ---------------------------------------------------------------------------
// (d) DETERMINISM — status over multiple --admin addresses must print in the
// USER-SUPPLIED order, deterministically across runs (no map ranging).
// ---------------------------------------------------------------------------

func TestStatus_MultiNode_DeterministicOrder(t *testing.T) {
	// Two distinct servers; the CLI must print them in the order given on --admin.
	mk := func(id string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"id":%q,"state":"Follower","isLeader":false,"leader":"n1","leaderAddr":"x"}`, id)
		}))
	}
	a := mk("nodeA")
	b := mk("nodeB")
	defer a.Close()
	defer b.Close()

	spec := hostport(a) + "," + hostport(b)
	var first string
	for i := 0; i < 5; i++ {
		code, out, errOut := runArgs("status", "--admin", spec)
		if code != exitOK {
			t.Fatalf("status multi: exit=%d stderr=%q", code, errOut)
		}
		ia := strings.Index(out, "id:         nodeA")
		ib := strings.Index(out, "id:         nodeB")
		if ia < 0 || ib < 0 || ia > ib {
			t.Fatalf("status multi: nodeA must print before nodeB; out=%q", out)
		}
		if first == "" {
			first = out
		} else if out != first {
			t.Fatalf("status multi: output not deterministic across runs:\nfirst=%q\nnow=%q", first, out)
		}
	}
	t.Logf("SINK status(2 nodes) deterministic & in --admin order across 5 runs")
}

// ---------------------------------------------------------------------------
// (c) UNGUARDED SHARED STATE — drive concurrent run() invocations under -race.
// Each run() builds its OWN *client, so there is no shared mutable CLI state; this
// pins that contract (no data race) honestly. Concurrency capped at 8.
// ---------------------------------------------------------------------------

func TestConcurrent_Run_NoRace(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"n1","state":"Leader","isLeader":true,"leader":"n1","leaderAddr":"x","numKeys":3}`))
	}))
	defer ts.Close()

	addr := hostport(ts)
	var wg sync.WaitGroup
	const workers = 8
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _, _ := runArgs("status", "--admin", addr)
			if code != exitOK {
				t.Errorf("concurrent status: exit=%d", code)
			}
		}()
	}
	wg.Wait()
	t.Logf("SINK %d concurrent run() invocations completed with no -race report", workers)
}
