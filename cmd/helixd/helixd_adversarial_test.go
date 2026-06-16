package main

// Adversarial test suite for cmd/helixd. Probes SINK-SIDE four defect classes:
//
//	(a) config/arg validation fail-open  — the actual accept/reject verdict
//	(b) teardown/shutdown race           — concurrent run+cancel under -race
//	(c) unguarded shared state           — concurrent checkServices map writes
//	(d) determinism                      — map-ordered wiring / JSON output
//
// Anti-hang: ephemeral (":0") listeners only, capped concurrency, every wait
// is bounded by a timeout. `go test -race -timeout 90s` MUST complete.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// (a) CONFIG / ARG VALIDATION FAIL-OPEN — sink = the accept/reject verdict.
// ---------------------------------------------------------------------------

// TestConfigValidate_RejectMatrix asserts Validate's verdict on a battery of
// boundary/invalid inputs. A fail-open (accepting an invalid config) is the
// bug we hunt; we assert the exact accept/reject boolean per row.
func TestConfigValidate_RejectMatrix(t *testing.T) {
	good := time.Second
	rows := []struct {
		name   string
		cfg    Config
		reject bool
	}{
		{"zero-value config (probe+shutdown == 0)", Config{}, true},
		{"port -1", Config{Port: -1, ProbeTimeout: good, ShutdownTimeout: good}, true},
		{"port 65536 (just over max)", Config{Port: 65536, ProbeTimeout: good, ShutdownTimeout: good}, true},
		{"port 70000", Config{Port: 70000, ProbeTimeout: good, ShutdownTimeout: good}, true},
		{"negative probe timeout", Config{Port: 8081, ProbeTimeout: -good, ShutdownTimeout: good}, true},
		{"zero shutdown timeout", Config{Port: 8081, ProbeTimeout: good, ShutdownTimeout: 0}, true},
		// Documented-valid rows (NOT fail-open — these are contractually accepted):
		{"port 0 ephemeral (documented, test-only)", Config{Port: 0, ProbeTimeout: good, ShutdownTimeout: good}, false},
		{"port 65535 (max boundary)", Config{Port: 65535, ProbeTimeout: good, ShutdownTimeout: good}, false},
		{"port 1 (min usable)", Config{Port: 1, ProbeTimeout: good, ShutdownTimeout: good}, false},
	}
	for _, r := range rows {
		err := r.cfg.Validate()
		gotReject := err != nil
		if gotReject != r.reject {
			t.Errorf("%s: Validate() rejected=%v (err=%v), want rejected=%v",
				r.name, gotReject, err, r.reject)
		}
	}
}

// TestLoadConfig_EnvFailOpen drives LoadConfig through env strings that an
// operator could plausibly set. The sink is whether a clearly-bad env is
// rejected (fail-closed) vs silently swallowed (fail-open).
func TestLoadConfig_EnvFailOpen(t *testing.T) {
	rows := []struct {
		name   string
		env    map[string]string
		reject bool
	}{
		{"empty env -> defaults accepted", map[string]string{}, false},
		{"non-numeric port", map[string]string{"HELIXD_PORT": "notaport"}, true},
		{"float port", map[string]string{"HELIXD_PORT": "80.5"}, true},
		{"negative port", map[string]string{"HELIXD_PORT": "-1"}, true},
		{"out-of-range port", map[string]string{"HELIXD_PORT": "99999"}, true},
		{"trailing junk", map[string]string{"HELIXD_PORT": "8081x"}, true},
		{"whitespace port", map[string]string{"HELIXD_PORT": " 8081 "}, true},
		{"valid port", map[string]string{"HELIXD_PORT": "9090"}, false},
	}
	for _, r := range rows {
		_, err := LoadConfig(func(k string) string { return r.env[k] })
		gotReject := err != nil
		if gotReject != r.reject {
			t.Errorf("%s: LoadConfig rejected=%v (err=%v), want rejected=%v",
				r.name, gotReject, err, r.reject)
		}
	}
}

// TestRun_RejectsBadConfigBeforeBind proves run() validates BEFORE binding a
// listener: a bad port must surface a "config" error, never a half-open
// listener. Sink = error string is the config-validation error.
func TestRun_RejectsBadConfigBeforeBind(t *testing.T) {
	bad := Config{Host: "127.0.0.1", Port: 65536, ProbeTimeout: time.Second, ShutdownTimeout: time.Second}
	err := run(context.Background(), bad, nil, fakeProbe(nil), nil)
	if err == nil {
		t.Fatal("expected config error for port 65536, got nil (FAIL-OPEN)")
	}
	if !strings.Contains(err.Error(), "config") {
		t.Errorf("error %q should be a config-validation error (not a bind error)", err)
	}
}

// ---------------------------------------------------------------------------
// (b) TEARDOWN / SHUTDOWN RACE — concurrent run+cancel; clean nil return, no
//     send-on-closed / double-close / hang. Capped at 8 concurrent daemons.
// ---------------------------------------------------------------------------

func TestRun_ConcurrentStartStop_NoRaceNoLeak(t *testing.T) {
	const n = 8 // capped concurrency
	deps := []Dependency{
		{Name: "etcd", Addr: "e:1"},
		{Name: "postgres", Addr: "p:1"},
		{Name: "redis", Addr: "r:1"},
	}
	probe := fakeProbe(map[string]bool{"e:1": true, "r:1": true})

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg := Config{
				Host:            "127.0.0.1",
				Port:            0, // ephemeral, host-safe
				ProbeTimeout:    200 * time.Millisecond,
				ShutdownTimeout: time.Second,
			}
			ctx, cancel := context.WithCancel(context.Background())
			addrCh := make(chan string, 1)
			runErr := make(chan error, 1)
			go func() { runErr <- run(ctx, cfg, deps, probe, func(a string) { addrCh <- a }) }()

			var addr string
			select {
			case addr = <-addrCh:
			case err := <-runErr:
				cancel()
				t.Errorf("run exited before ready: %v", err)
				return
			case <-time.After(5 * time.Second):
				cancel()
				t.Error("timed out waiting for ready")
				return
			}

			// Hit it once to have an in-flight-ish request, then tear down.
			if resp, err := http.Get("http://" + addr + "/status"); err == nil {
				resp.Body.Close()
			}
			cancel() // trigger graceful shutdown

			select {
			case err := <-runErr:
				if err != nil {
					t.Errorf("clean shutdown returned error: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("run did not return after cancel (hang / unbounded shutdown)")
			}

			// Sink: listener must be closed post-shutdown.
			if c, derr := net.DialTimeout("tcp", addr, 300*time.Millisecond); derr == nil {
				c.Close()
				t.Errorf("server still accepting on %s after shutdown", addr)
			}
		}()
	}
	wg.Wait()
}

// TestRun_CancelBeforeReady stresses the path where ctx is already cancelled
// when run starts: it must still bind, observe ctx.Done, shut down cleanly,
// and return nil — never hang or double-close.
func TestRun_CancelBeforeReady(t *testing.T) {
	deps := []Dependency{{Name: "etcd", Addr: "e:1"}}
	probe := fakeProbe(map[string]bool{"e:1": true})
	cfg := Config{Host: "127.0.0.1", Port: 0, ProbeTimeout: 100 * time.Millisecond, ShutdownTimeout: time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already done

	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, cfg, deps, probe, nil) }()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("run with pre-cancelled ctx returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run with pre-cancelled ctx did not return (hang)")
	}
}

// ---------------------------------------------------------------------------
// (c) UNGUARDED SHARED STATE — checkServices fans out one goroutine per dep
//     writing a shared map. -race must stay clean with many concurrent writers.
// ---------------------------------------------------------------------------

func TestCheckServices_ConcurrentMapWrites_RaceClean(t *testing.T) {
	const ndeps = 64
	deps := make([]Dependency, ndeps)
	reachable := map[string]bool{}
	want := map[string]string{}
	for i := 0; i < ndeps; i++ {
		name := fmt.Sprintf("svc%02d", i)
		addr := fmt.Sprintf("h:%d", i)
		deps[i] = Dependency{Name: name, Addr: addr}
		if i%2 == 0 {
			reachable[addr] = true
			want[name] = stateReachable
		} else {
			want[name] = stateUnreachable
		}
	}
	probe := fakeProbe(reachable)
	got := checkServices(context.Background(), deps, probe, time.Second)

	if len(got) != ndeps {
		t.Fatalf("got %d entries, want %d (lost a concurrent write?)", len(got), ndeps)
	}
	for name, st := range want {
		if got[name] != st {
			t.Errorf("svc %s = %q, want %q", name, got[name], st)
		}
	}
}

// TestStatusHandler_ConcurrentRequests_RaceClean hammers the real /status
// handler from multiple clients concurrently. Each request re-probes; shared
// state (deps slice, probe) must be read-safe. -race is the sink.
func TestStatusHandler_ConcurrentRequests_RaceClean(t *testing.T) {
	deps := []Dependency{
		{Name: "etcd", Addr: "e:1"},
		{Name: "postgres", Addr: "p:1"},
		{Name: "redis", Addr: "r:1"},
	}
	probe := fakeProbe(map[string]bool{"e:1": true, "r:1": true})
	srv := httptest.NewServer(newMux(deps, probe, 200*time.Millisecond))
	defer srv.Close()

	const workers = 8
	const each = 10
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				resp, err := http.Get(srv.URL + "/status")
				if err != nil {
					t.Errorf("GET /status: %v", err)
					return
				}
				var body status
				err = json.NewDecoder(resp.Body).Decode(&body)
				resp.Body.Close()
				if err != nil {
					t.Errorf("decode: %v", err)
					return
				}
				if body.Services["postgres"] != stateUnreachable {
					t.Errorf("postgres = %q, want unreachable", body.Services["postgres"])
				}
			}
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// (d) DETERMINISM — map-ordered wiring / JSON output must be stable.
// ---------------------------------------------------------------------------

// TestDependencyNames_Deterministic asserts the startup-banner names are
// sorted and order-independent of input slice order.
func TestDependencyNames_Deterministic(t *testing.T) {
	a := dependencyNames([]Dependency{{Name: "redis"}, {Name: "etcd"}, {Name: "postgres"}})
	b := dependencyNames([]Dependency{{Name: "postgres"}, {Name: "redis"}, {Name: "etcd"}})
	want := []string{"etcd", "postgres", "redis"}
	for _, got := range [][]string{a, b} {
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v (not sorted/deterministic)", got, want)
			}
		}
	}
}

// TestStatusPayload_StableJSON encodes the same status repeatedly and asserts
// byte-identical output (encoding/json sorts map keys, so /status is stable).
func TestStatusPayload_StableJSON(t *testing.T) {
	s := status{
		Version:  version,
		Status:   "healthy",
		Services: map[string]string{"redis": stateReachable, "etcd": stateUnreachable, "postgres": stateReachable},
		// fixed timestamp so only map-ordering can vary the bytes
		Timestamp: 1700000000,
	}
	var first []byte
	for i := 0; i < 32; i++ {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(s); err != nil {
			t.Fatalf("encode: %v", err)
		}
		if i == 0 {
			first = append([]byte(nil), buf.Bytes()...)
			continue
		}
		if !bytes.Equal(first, buf.Bytes()) {
			t.Fatalf("non-deterministic /status JSON:\n first=%s\n iter%d=%s", first, i, buf.Bytes())
		}
	}
	// Sanity: keys are present and sorted as encoding/json guarantees.
	if !bytes.Contains(first, []byte(`"etcd"`)) {
		t.Errorf("payload missing etcd: %s", first)
	}
}
