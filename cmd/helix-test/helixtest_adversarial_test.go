package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Adversarial probes for cmd/helix-test (the QA/Challenge runner).
//
// Highest-value class per CLAUDE-1: a FALSE-PASS / fail-open — the harness
// reporting PASS (exit 0) when the sink-side proof the Challenge promises was
// NOT actually produced.
//
// challenge.go's own contract (file header, lines 4-6) states a Challenge:
//   "...writes an evidence file (JSON) that includes the captured sink line
//    and a per-run UUID, then exits 0 on pass / non-zero on fail."
// The evidence file is the REQUIRED sink-side artifact (CLAUDE-1 rule 6:
// "captured evidence ... proving end-user-visible operation"). A PASS with no
// evidence written is a PASS-bluff.
// ---------------------------------------------------------------------------

func adversarialServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

// TestAdversarial_FalsePASS_EvidenceWriteFailureStillReportsPASS is the core
// CLAUDE-1 probe. The HTTP probe succeeds and the sink matches, but the
// evidence directory CANNOT be created (its parent path is a regular file, so
// os.MkdirAll fails). writeEvidence then returns "" — NO evidence is on disk.
//
// SINK-SIDE verdict asserted: a Challenge that could not capture its required
// evidence MUST NOT report PASS with exit 0. If it does, the operator/CI gets a
// green Challenge with zero proof — exactly the PASS-bluff CLAUDE-1 forbids.
//
// On UNFIXED prod this test FAILS (code==0, evidencePath==""), proving the bug.
func TestAdversarial_FalsePASS_EvidenceWriteFailureStillReportsPASS(t *testing.T) {
	ts := adversarialServer(t, "helix status=OK live=true")

	// Make a path that is a regular FILE; then ask for evidence in a subdir of
	// it. os.MkdirAll(filePath/sub) must fail with ENOTDIR on every OS.
	base := t.TempDir()
	notADir := filepath.Join(base, "iam-a-file")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	brokenEvidenceDir := filepath.Join(notADir, "evidence-subdir")

	var out, errb bytes.Buffer
	cfg := ChallengeConfig{
		URL:          ts.URL,
		ExpectedSink: "status=OK",
		EvidenceDir:  brokenEvidenceDir,
		Name:         "false-pass-probe",
		Client:       ts.Client(),
	}
	evidencePath, code := RunChallenge(cfg, &out, &errb)

	// First confirm the precondition really triggered: no evidence on disk.
	evidenceWritten := evidencePath != ""
	if evidenceWritten {
		if _, err := os.Stat(evidencePath); err != nil {
			evidenceWritten = false
		}
	}
	if evidenceWritten {
		t.Skip("precondition not met: evidence unexpectedly writable; cannot probe fail-open")
	}

	// SINK-SIDE verdict: with no evidence captured, exit MUST be non-zero and
	// stdout MUST NOT claim PASS. This is the CLAUDE-1 sink assertion.
	if code == 0 {
		t.Fatalf("FALSE-PASS: RunChallenge returned exit 0 with NO evidence file written "+
			"(evidencePath=%q). A Challenge that cannot capture its required sink-side "+
			"evidence must FAIL, not silently PASS. stdout=%q stderr=%q",
			evidencePath, out.String(), errb.String())
	}
	if strings.Contains(out.String(), "PASS") {
		t.Fatalf("FALSE-PASS: stdout reported PASS with no evidence on disk: %q", out.String())
	}
}

// TestAdversarial_Contract_PassWritesEvidence pins the honest contract: when
// evidence CAN be written, a matching probe both writes the file AND exits 0.
// (Contract guard so a fix to the fail-open cannot regress the happy path.)
func TestAdversarial_Contract_PassWritesEvidence(t *testing.T) {
	ts := adversarialServer(t, "helix status=OK live=true")
	dir := t.TempDir()

	var out, errb bytes.Buffer
	cfg := ChallengeConfig{
		URL:          ts.URL,
		ExpectedSink: "status=OK",
		EvidenceDir:  dir,
		Name:         "contract-pass",
		Client:       ts.Client(),
	}
	evidencePath, code := RunChallenge(cfg, &out, &errb)
	if code != 0 {
		t.Fatalf("happy-path challenge must PASS, got exit=%d stderr=%q", code, errb.String())
	}
	if evidencePath == "" {
		t.Fatal("happy-path challenge: evidencePath empty despite writable dir")
	}
	if _, err := os.Stat(evidencePath); err != nil {
		t.Fatalf("happy-path evidence file missing: %v", err)
	}
}

// TestAdversarial_NoFalsePASS_WrongSink confirms AND-semantics on the sink
// assertion: a non-matching body must FAIL (exit!=0). Guards against any fold
// that would let a failing sink check be reported as PASS.
func TestAdversarial_NoFalsePASS_WrongSink(t *testing.T) {
	ts := adversarialServer(t, "error: degraded")
	dir := t.TempDir()

	var out, errb bytes.Buffer
	cfg := ChallengeConfig{
		URL:          ts.URL,
		ExpectedSink: "status=OK",
		EvidenceDir:  dir,
		Name:         "wrong-sink",
		Client:       ts.Client(),
	}
	evidencePath, code := RunChallenge(cfg, &out, &errb)
	if code == 0 {
		t.Fatalf("wrong-sink challenge reported PASS (exit 0); must FAIL. stdout=%q", out.String())
	}
	ev := readEvidence(t, evidencePath)
	if ev.Status != "FAIL" {
		t.Errorf("wrong-sink evidence status=%q, want FAIL", ev.Status)
	}
}

// TestAdversarial_ConcurrentChallenges_NoLostEvidence runs many Challenges
// concurrently (capped) into a shared evidence dir, under -race. The runner
// holds no shared results map of its own, but this proves: (1) no data race in
// the per-run path, and (2) CONSERVATION — every PASS run that returns exit 0
// has a distinct, on-disk evidence file (no lost/overwritten result). A unique
// per-run UUID in the filename is what guarantees no collision.
func TestAdversarial_ConcurrentChallenges_NoLostEvidence(t *testing.T) {
	ts := adversarialServer(t, "status=OK")
	dir := t.TempDir()

	const n = 8 // concurrency cap <= 8 per anti-hang
	var wg sync.WaitGroup
	var mu sync.Mutex
	paths := make(map[string]struct{}, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var out, errb bytes.Buffer
			cfg := ChallengeConfig{
				URL:          ts.URL,
				ExpectedSink: "status=OK",
				EvidenceDir:  dir,
				Name:         "conc",
				Client:       ts.Client(),
			}
			p, code := RunChallenge(cfg, &out, &errb)
			if code != 0 {
				t.Errorf("concurrent run %d unexpectedly failed: stderr=%q", i, errb.String())
				return
			}
			mu.Lock()
			paths[p] = struct{}{}
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	// Conservation: n successful runs => n distinct evidence files on disk.
	if len(paths) != n {
		t.Fatalf("evidence conservation: got %d distinct evidence paths for %d PASS runs (lost/collided result)", len(paths), n)
	}
	for p := range paths {
		if p == "" {
			t.Fatal("a PASS run produced an empty evidence path")
		}
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("evidence file for a PASS run missing on disk: %v", err)
		}
	}
}
