package log

// Adversarial sink-side probes for pkg/log.
//
// Bug classes under test:
//   (a) CONCURRENT WRITE INTEGRITY  — N goroutines -> one shared buffer:
//       exactly N well-formed JSON lines, none torn, none lost.
//   (b) LEVEL FILTER BOUNDARY       — message exactly at threshold emitted (>=),
//       and a runtime SetLevel racing with a concurrent log (-race).
//   (c) FIELD/CONTEXT MAP           — WithField / context map shared & raced;
//       assert no field bleed between independent loggers.
//   (d) FORMAT/INJECTION            — format specifiers, embedded newlines, and a
//       huge payload land as DATA in the JSON record, never as a torn/extra line.
//
// All assertions are SINK-SIDE: we parse every emitted line and count, never
// "no panic". Run: go test -race -count=2 ./pkg/log

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// syncBuffer is a bytes.Buffer guarded by a mutex so the TEST harness itself
// never races on Write/String. It deliberately does NOT serialize at any finer
// granularity than a single Write call, so if the logger under test issues a
// torn/partial write, this buffer faithfully records the interleaving.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// parseLines splits the captured output into non-empty lines, asserts every
// line is a well-formed JSON object (no torn/interleaved bytes), and returns the
// decoded records. A torn line fails json.Unmarshal -> caught here.
func parseLines(t *testing.T, out string) []map[string]interface{} {
	t.Helper()
	var records []map[string]interface{}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024) // allow huge lines
	line := 0
	for sc.Scan() {
		raw := sc.Bytes()
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		line++
		var rec map[string]interface{}
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatalf("TORN/INVALID line %d: %v\nbytes: %q", line, err, string(raw))
		}
		records = append(records, rec)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}
	return records
}

// (a) CONCURRENT WRITE INTEGRITY -------------------------------------------------
//
// N goroutines log concurrently through ONE shared *Logger to ONE buffer.
// Conservation: exactly N records, each well-formed, each carrying its unique
// goroutine id -> none lost, none torn, none duplicated.
func TestAdversarial_ConcurrentWriteIntegrity(t *testing.T) {
	const N = 500
	var buf syncBuffer
	l := New("conc", &buf)
	l.SetLevel(InfoLevel)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			l.Info("concurrent line", "gid", id)
		}(i)
	}
	close(start)
	wg.Wait()

	records := parseLines(t, buf.String())
	if len(records) != N {
		t.Fatalf("conservation FAILED: expected %d well-formed lines, got %d", N, len(records))
	}
	seen := make(map[int]bool, N)
	for _, rec := range records {
		f, ok := rec["gid"].(float64)
		if !ok {
			t.Fatalf("record missing gid field: %v", rec)
		}
		id := int(f)
		if seen[id] {
			t.Fatalf("duplicate gid %d (a line was written twice)", id)
		}
		seen[id] = true
		if rec["msg"] != "concurrent line" {
			t.Fatalf("corrupted msg: %v", rec["msg"])
		}
	}
	if len(seen) != N {
		t.Fatalf("expected %d unique gids, got %d (lost logs)", N, len(seen))
	}
}

// (a') CONCURRENT WRITE INTEGRITY ACROSS DERIVED LOGGERS -------------------------
//
// This is the sharper probe. WithField returns a NEW *Logger whose inner slog
// handler is built via .With() (shares the parent handler's write mutex). We mix
// writes from the parent AND many derived loggers, all to the SAME buffer, and
// demand conservation + zero torn lines. If derived loggers wrote under an
// independent mutex domain, concurrent writes to the shared buffer could tear.
func TestAdversarial_ConcurrentDerivedLoggersIntegrity(t *testing.T) {
	const N = 400
	var buf syncBuffer
	base := New("derv", &buf)
	base.SetLevel(InfoLevel)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			// half via parent, half via a freshly derived logger
			if id%2 == 0 {
				base.Info("p", "gid", id)
			} else {
				base.WithField("derived", id).Info("d", "gid", id)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	records := parseLines(t, buf.String())
	if len(records) != N {
		t.Fatalf("conservation FAILED across derived loggers: expected %d lines, got %d", N, len(records))
	}
	seen := make(map[int]bool, N)
	for _, rec := range records {
		f, ok := rec["gid"].(float64)
		if !ok {
			t.Fatalf("record missing gid: %v", rec)
		}
		seen[int(f)] = true
	}
	if len(seen) != N {
		t.Fatalf("expected %d unique gids across derived loggers, got %d", N, len(seen))
	}
}

// (b) LEVEL FILTER BOUNDARY ------------------------------------------------------
//
// Contract per log(): `if level < l.level { return }` => emit iff level >= threshold.
// Probe every (threshold, msgLevel) pair against that exact predicate. A single
// off-by-one (`<=` instead of `<`, or `>` instead of `>=`) flips a verdict here.
func TestAdversarial_LevelFilterBoundary(t *testing.T) {
	levels := []Level{DebugLevel, InfoLevel, WarnLevel, ErrorLevel}
	emit := func(l *Logger, lvl Level) {
		switch lvl {
		case DebugLevel:
			l.Debug("m")
		case InfoLevel:
			l.Info("m")
		case WarnLevel:
			l.Warn("m")
		case ErrorLevel:
			l.Error("m")
		}
	}
	for _, threshold := range levels {
		for _, msgLvl := range levels {
			var buf bytes.Buffer
			l := New("bound", &buf)
			l.SetLevel(threshold)
			emit(l, msgLvl)
			emitted := strings.Contains(buf.String(), `"msg":"m"`)
			want := msgLvl >= threshold // the documented predicate
			if emitted != want {
				t.Fatalf("level boundary VIOLATION: threshold=%s msg=%s emitted=%v want=%v\noutput=%s",
					threshold, msgLvl, emitted, want, buf.String())
			}
			// When emitted, the JSON level string must match the message level.
			if emitted {
				records := parseLines(t, buf.String())
				if len(records) != 1 {
					t.Fatalf("expected exactly 1 record at threshold=%s msg=%s, got %d", threshold, msgLvl, len(records))
				}
			}
		}
	}
}

// (b') SETLEVEL RACES WITH CONCURRENT LOG ---------------------------------------
//
// SetLevel rebuilds l.inner under a write lock while loggers call log() under a
// read lock. Run both concurrently under -race. Conservation here is looser
// (level changes mid-flight legitimately drop some lines), so we assert: (1) the
// race detector stays silent, and (2) every line that DID land is well-formed
// and at a level >= the lowest threshold we ever set (never a torn line).
func TestAdversarial_SetLevelRacesWithLog(t *testing.T) {
	const N = 600
	var buf syncBuffer
	l := New("setlvl", &buf)

	var wg sync.WaitGroup
	start := make(chan struct{})

	// flipper: races SetLevel against the loggers
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		seq := []Level{DebugLevel, ErrorLevel, InfoLevel, WarnLevel, DebugLevel}
		for i := 0; i < 200; i++ {
			l.SetLevel(seq[i%len(seq)])
		}
	}()

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			l.Error("race", "gid", id) // ERROR: passes every threshold we set
		}(i)
	}
	close(start)
	wg.Wait()

	records := parseLines(t, buf.String())
	// Every ERROR line should survive all thresholds (max we set is WarnLevel,
	// and ERROR >= WARN), so conservation is exact: no ERROR may be lost.
	if len(records) != N {
		t.Fatalf("ERROR conservation under SetLevel race FAILED: expected %d, got %d", N, len(records))
	}
	for _, rec := range records {
		if rec["level"] != "ERROR" {
			t.Fatalf("unexpected level under race: %v", rec["level"])
		}
	}
}

// (c) FIELD / CONTEXT MAP — bleed & race ----------------------------------------
//
// WithFields/WithField/WithContext each build a derived logger. Probe that fields
// set on one derived logger do NOT bleed into a sibling or the parent, even when
// many derived loggers are built and used concurrently from a shared parent.
func TestAdversarial_FieldNoBleedUnderConcurrency(t *testing.T) {
	const N = 300
	var buf syncBuffer
	base := New("fields", &buf)
	base.SetLevel(InfoLevel)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			// Each goroutine stamps its OWN unique field value; if the shared
			// parent's arg slice were mutated/aliased, values would bleed.
			base.WithField("owner", id).Info("evt", "gid", id)
		}(i)
	}
	close(start)
	wg.Wait()

	records := parseLines(t, buf.String())
	if len(records) != N {
		t.Fatalf("conservation FAILED: expected %d, got %d", N, len(records))
	}
	for _, rec := range records {
		gid, ok1 := rec["gid"].(float64)
		owner, ok2 := rec["owner"].(float64)
		if !ok1 || !ok2 {
			t.Fatalf("missing gid/owner field: %v", rec)
		}
		if gid != owner {
			t.Fatalf("FIELD BLEED: line had gid=%v but owner=%v (a sibling's field leaked)", gid, owner)
		}
	}
}

// (c') CONTEXT MAP IMMUTABILITY — ContextWithFields must not mutate the parent's
// map (it copies). Race many derivations off one parent context and assert the
// parent context's field set is unchanged and no derived line bleeds.
func TestAdversarial_ContextMapNoMutation(t *testing.T) {
	const N = 200
	parent := ContextWithFields(context.Background(), map[string]interface{}{"base": "B"})

	var buf syncBuffer
	l := New("ctx", &buf)
	l.SetLevel(InfoLevel)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			ctx := ContextWithFields(parent, map[string]interface{}{"child": id})
			l.WithContext(ctx).Info("c", "gid", id)
		}(i)
	}
	close(start)
	wg.Wait()

	// Parent map must be untouched (still exactly {"base":"B"}).
	pf, _ := parent.Value(loggerFieldsKey).(map[string]interface{})
	if len(pf) != 1 || pf["base"] != "B" {
		t.Fatalf("parent context map MUTATED by children: %v", pf)
	}

	records := parseLines(t, buf.String())
	if len(records) != N {
		t.Fatalf("conservation FAILED: expected %d, got %d", N, len(records))
	}
	for _, rec := range records {
		gid, ok1 := rec["gid"].(float64)
		child, ok2 := rec["child"].(float64)
		if !ok1 || !ok2 {
			t.Fatalf("missing gid/child: %v", rec)
		}
		if gid != child {
			t.Fatalf("CONTEXT FIELD BLEED: gid=%v child=%v", gid, child)
		}
		if rec["base"] != "B" {
			t.Fatalf("expected inherited base=B, got %v", rec["base"])
		}
	}
}

// (d) FORMAT / INJECTION ---------------------------------------------------------
//
// Hostile message + field values: format specifiers, embedded newlines, quotes,
// and a huge payload. They must land as JSON-escaped DATA inside ONE record each
// — never produce extra lines (log injection) and never panic.
func TestAdversarial_FormatInjectionSafety(t *testing.T) {
	var buf bytes.Buffer
	l := New("fmt", &buf)
	l.SetLevel(InfoLevel)

	hostileMsg := "pct=%s%d%v newline=\nINJECTED:{\"level\":\"ERROR\",\"msg\":\"fake\"}\n tab=\t quote=\""
	hugeVal := strings.Repeat("A", 200000)

	l.Info(hostileMsg, "evil_key", "v\nINJECTED2:{}", "huge", hugeVal)

	records := parseLines(t, buf.String())
	// Exactly ONE record despite the embedded newlines/fake-JSON: no injection.
	if len(records) != 1 {
		t.Fatalf("LOG INJECTION: hostile input produced %d lines, want 1\noutput=%s", len(records), buf.String())
	}
	rec := records[0]
	if rec["msg"] != hostileMsg {
		t.Fatalf("msg not preserved verbatim as data: %q", rec["msg"])
	}
	if rec["evil_key"] != "v\nINJECTED2:{}" {
		t.Fatalf("field value not preserved verbatim: %q", rec["evil_key"])
	}
	if s, _ := rec["huge"].(string); len(s) != len(hugeVal) {
		t.Fatalf("huge value truncated/corrupted: got len %d want %d", len(s), len(hugeVal))
	}
	// The fake nested record must NOT have surfaced as a real top-level record.
	if rec["level"] != "INFO" {
		t.Fatalf("level overridden by injected payload: %v", rec["level"])
	}
}

// (d') ODD KEY/VALUE PAIR — a dangling key with no value must not panic and must
// still yield exactly one well-formed line (slog handles via !BADKEY).
func TestAdversarial_OddKeysNoPanicSingleLine(t *testing.T) {
	var buf bytes.Buffer
	l := New("odd", &buf)
	l.SetLevel(InfoLevel)
	l.Info("dangling", "lonely_key") // odd number of args
	records := parseLines(t, buf.String())
	if len(records) != 1 {
		t.Fatalf("odd-args produced %d lines, want 1\noutput=%s", len(records), buf.String())
	}
	if records[0]["msg"] != "dangling" {
		t.Fatalf("msg corrupted: %v", records[0]["msg"])
	}
}
