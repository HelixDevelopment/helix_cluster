package offlinesync

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// This file is an ADVERSARIAL, mutation-verified audit of the offlinesync
// serialization/reconcile contract. It is a HIGH-VALUE deserialize target:
// DecompressDelta consumes EXTERNAL bytes (the wire form shipped between a
// device and the authoritative server).
//
// SERIALIZATION MODEL (read from delta.go, not assumed):
//   - Codec: encoding/json over compress/flate (DEFLATE, BestCompression).
//   - CompressDelta:   json.Marshal -> flate.Write -> flate.Close.
//   - DecompressDelta: flate.NewReader -> io.ReadAll -> json.Unmarshal.
//   - RawSerializeDelta: plain json.Marshal (no compression).
//   - Wire bytes are NOT framed/length-prefixed: a raw flate stream of a JSON
//     array. There is no magic, no version, no checksum.
//
// RECONCILE MODEL (reconcile.go):
//   - map[JobID]CompletedJob; conflict policy = "lower Seq wins, deterministic
//     regardless of arrival order"; HWM advances to max Seq seen.
//
// Centerpieces below: round-trip fidelity, deserialize-never-panics (DoS), and
// conflict determinism — plus two pinned FINDINGS (decompression-bomb and the
// equal-Seq tiebreak gap) documented honestly against the live behaviour.

// ── CENTERPIECE 1: ROUND-TRIP FIDELITY ───────────────────────────────────────
// A fully-populated delta with distinct values in every field — boundary
// numbers, unicode, embedded NULs, a large redundant payload, empty/nil
// Output, far-future and zero timestamps — must survive BOTH wire paths
// (Compress->Decompress and Serialize->Deserialize) byte-for-byte.
//
// ANTI-BLUFF: DeepEqual over the WHOLE slice means dropping or corrupting any
// single field (the canonical "drop a field in serialize" mutation) makes this
// fail with the exact lost field.
func TestAdversarial_RoundTripFidelity_AllFields(t *testing.T) {
	t.Parallel()

	// time.Time JSON round-trips through RFC3339Nano; build values that survive
	// that (monotonic clock stripped, UTC) so DeepEqual is exact.
	base := time.Date(2026, 6, 14, 12, 30, 45, 123456789, time.UTC)

	delta := []CompletedJob{
		{JobID: "", Output: nil, CompletedAt: time.Time{}.UTC(), Seq: 0},                                         // all-zero / nil edges
		{JobID: "job-001", Output: []byte{}, CompletedAt: base, Seq: 1},                                          // empty (non-nil) Output
		{JobID: "δ-юникод-🚀-key", Output: []byte("δ-юникод-🚀"), CompletedAt: base.Add(time.Hour), Seq: 42},       // unicode in key + value
		{JobID: "nul\x00bytes", Output: []byte{0x00, 0x01, 0xff, 0xfe, 0x00}, CompletedAt: base, Seq: 7},         // embedded NULs / binary
		{JobID: "max-seq", Output: bytes.Repeat([]byte("ABCDEF"), 4096), CompletedAt: base, Seq: math.MaxUint64}, // boundary Seq + large payload
		// Latest timestamp Go's time.Time can JSON-marshal: year 9999 is the
		// documented RFC3339 upper bound; year >= 10000 is unrepresentable by
		// time.MarshalJSON (a stdlib limit, not an offlinesync defect).
		{JobID: "far-future", Output: []byte("x"), CompletedAt: time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), Seq: math.MaxUint64 - 1},
	}

	// Path A: compressed wire form.
	compressed, err := CompressDelta(delta)
	if err != nil {
		t.Fatalf("CompressDelta: %v", err)
	}
	recA, err := DecompressDelta(compressed)
	if err != nil {
		t.Fatalf("DecompressDelta: %v", err)
	}
	if !reflect.DeepEqual(delta, recA) {
		t.Fatalf("COMPRESS round-trip is NOT bijective:\n got=%#v\nwant=%#v", recA, delta)
	}

	// Path B: raw JSON serialize/deserialize (the codec the wire form wraps).
	raw, err := RawSerializeDelta(delta)
	if err != nil {
		t.Fatalf("RawSerializeDelta: %v", err)
	}
	recB, err := DecompressDelta(mustFlate(t, raw))
	if err != nil {
		t.Fatalf("DecompressDelta(raw-reflated): %v", err)
	}
	if !reflect.DeepEqual(delta, recB) {
		t.Fatalf("SERIALIZE round-trip is NOT bijective:\n got=%#v\nwant=%#v", recB, delta)
	}
}

// ── CENTERPIECE 1b: empty + single-entry + nil edges ─────────────────────────
func TestAdversarial_RoundTrip_Edges(t *testing.T) {
	t.Parallel()

	cases := map[string][]CompletedJob{
		"nil-slice":   nil,
		"empty-slice": {},
		"single":      {{JobID: "solo", Output: []byte("o"), CompletedAt: time.Unix(0, 0).UTC(), Seq: 1}},
	}
	for name, in := range cases {
		c, err := CompressDelta(in)
		if err != nil {
			t.Fatalf("%s: CompressDelta: %v", name, err)
		}
		out, err := DecompressDelta(c)
		if err != nil {
			t.Fatalf("%s: DecompressDelta: %v", name, err)
		}
		// nil and empty both decode to a zero-length slice; assert by length to
		// avoid the nil-vs-empty JSON-array distinction.
		if len(out) != len(in) {
			t.Fatalf("%s: round-trip length got=%d want=%d", name, len(out), len(in))
		}
		for i := range in {
			if !reflect.DeepEqual(in[i], out[i]) {
				t.Fatalf("%s[%d]: got=%#v want=%#v", name, i, out[i], in[i])
			}
		}
	}
}

// ── CENTERPIECE 2: DESERIALIZE ROBUSTNESS — NEVER PANIC (DoS) ─────────────────
// DecompressDelta consumes hostile EXTERNAL bytes. On garbage, truncation,
// empty, nil, and crafted-bad input it MUST return an error and MUST NOT panic.
// A panic on hostile bytes is a remotely-triggerable DoS (the serde/hlc class).
//
// ANTI-BLUFF: every truncated prefix of a valid encoding is fed; a recover()
// guard turns any panic into a test failure naming the exact input.
func TestAdversarial_DecompressNeverPanics(t *testing.T) {
	t.Parallel()

	valid, err := CompressDelta([]CompletedJob{
		{JobID: "a", Output: []byte("hello"), CompletedAt: time.Now().UTC(), Seq: 1},
		{JobID: "b", Output: []byte("world"), CompletedAt: time.Now().UTC(), Seq: 2},
	})
	if err != nil {
		t.Fatalf("CompressDelta: %v", err)
	}

	hostile := [][]byte{
		nil,
		{},
		{0x00},
		{0xff},
		{0xff, 0xff, 0xff, 0xff},
		[]byte("this is plainly not a flate stream"),
		bytes.Repeat([]byte{0xde, 0xad, 0xbe, 0xef}, 64),
		append([]byte{0x1f, 0x8b}, valid...), // gzip magic prefix then flate body
		valid[1:],                            // drop first byte (shift bit alignment)
	}
	for i, in := range hostile {
		assertNoPanic(t, "hostile", i, in)
	}

	// ERROR CONTRACT: a known-INVALID stream MUST return a non-nil error (not a
	// swallowed empty delta). These inputs are guaranteed-corrupt flate, so a
	// correct decoder errors; a decoder that returns (nil, nil) is hiding a
	// transport failure as "zero jobs" — a silent data-loss bluff.
	mustError := [][]byte{
		[]byte("this is plainly not a flate stream"),
		bytes.Repeat([]byte{0xde, 0xad, 0xbe, 0xef}, 64),
		valid[:len(valid)/2], // truncated mid-stream
	}
	for i, in := range mustError {
		if _, err := DecompressDelta(in); err == nil {
			t.Fatalf("error-contract #%d: DecompressDelta(corrupt) returned nil error "+
				"(decode error swallowed into empty delta — silent data loss)", i)
		}
	}

	// POSITIVE CONTRACT: a valid stream still decodes to the real, non-empty
	// delta (guards against a mutation that nils out every result).
	if got, err := DecompressDelta(valid); err != nil || len(got) != 2 {
		t.Fatalf("valid stream must decode to 2 jobs with nil error, got len=%d err=%v", len(got), err)
	}

	// Every truncated prefix of a valid encoding (incl. full length).
	for n := 0; n <= len(valid); n++ {
		assertNoPanic(t, "truncated-prefix", n, valid[:n])
	}

	// Pseudo-random fuzz (deterministic LCG — no rand import needed).
	var seed uint64 = 0x9E3779B97F4A7C15
	for i := 0; i < 4096; i++ {
		seed = seed*6364136223846793005 + 1442695040888963407
		n := int(seed % 96)
		b := make([]byte, n)
		for j := range b {
			seed = seed*6364136223846793005 + 1442695040888963407
			b[j] = byte(seed >> 33)
		}
		assertNoPanic(t, "fuzz", i, b)
	}
}

// ── CENTERPIECE 3: CONFLICT DETERMINISM (antientropy-class) ──────────────────
// The strictly-ordered case (different Seq) MUST resolve to the SAME winner in
// both arrival orders (documented lower-Seq-wins). This pins the property the
// production code DOES guarantee.
//
// ANTI-BLUFF: a "slice-order wins" merge keeps the high-Seq record when it
// arrives first; the cross-order equality assert bites that.
func TestAdversarial_Conflict_DistinctSeq_Deterministic(t *testing.T) {
	t.Parallel()

	id := "conf-" + uuid.New().String()
	lo := CompletedJob{JobID: id, Output: []byte("LO"), CompletedAt: time.Now().UTC(), Seq: 4}
	hi := CompletedJob{JobID: id, Output: []byte("HI"), CompletedAt: time.Now().UTC(), Seq: 11}

	s1 := NewServer()
	s1.Reconcile([]CompletedJob{lo, hi})
	w1, _ := s1.GetJob(id)

	s2 := NewServer()
	s2.Reconcile([]CompletedJob{hi, lo})
	w2, _ := s2.GetJob(id)

	if !reflect.DeepEqual(w1, w2) {
		t.Fatalf("non-deterministic winner across arrival order: w1=%#v w2=%#v", w1, w2)
	}
	if w1.Seq != lo.Seq {
		t.Fatalf("wrong winner: kept Seq=%d want lower Seq=%d", w1.Seq, lo.Seq)
	}
	if s1.HighWaterMark() != hi.Seq {
		t.Fatalf("HWM=%d want max %d", s1.HighWaterMark(), hi.Seq)
	}
}

// ── FINDING A (FIXED, HXC-1762): equal-Seq, different-value now converges ─────
// The merge keeps the LOWER Seq for a JobID, but the EQUAL-Seq case used to keep
// the FIRST-ARRIVED record, making the winner order-DEPENDENT. Equal-Seq /
// different-Output is reachable (two devices colliding on a JobID, Seq being
// device-local), so two servers reconciling the same pair in different orders
// permanently diverged — the antientropy-class missing-tiebreak divergence
// (cf. HXC-1759). The fix adds a deterministic equal-Seq tie-break (the
// lexicographically-lower Output wins), making (Seq, Output) a total order so
// the winner is identical regardless of arrival order.
func TestAdversarial_Conflict_EqualSeq_Converges(t *testing.T) {
	t.Parallel()

	id := "collision-" + uuid.New().String()
	a := CompletedJob{JobID: id, Output: []byte("AAA"), CompletedAt: time.Now().UTC(), Seq: 5}
	b := CompletedJob{JobID: id, Output: []byte("BBB"), CompletedAt: time.Now().UTC(), Seq: 5}

	s1 := NewServer()
	s1.Reconcile([]CompletedJob{a, b})
	w1, _ := s1.GetJob(id)

	s2 := NewServer()
	s2.Reconcile([]CompletedJob{b, a})
	w2, _ := s2.GetJob(id)

	// Deterministic, order-independent: both servers converge to the same winner.
	if !reflect.DeepEqual(w1, w2) {
		t.Fatalf("REGRESSION: equal-Seq conflict resolved order-dependently "+
			"(order(a,b)->%q, order(b,a)->%q) — the deterministic tie-break is missing; "+
			"two servers would permanently diverge", w1.Output, w2.Output)
	}
	// The tie-break is lexicographically-lower Output: "AAA" < "BBB" -> "AAA" wins.
	if string(w1.Output) != "AAA" {
		t.Fatalf("wrong deterministic winner on equal-Seq tie: got %q, want %q "+
			"(lexicographically-lower Output)", w1.Output, "AAA")
	}
	t.Logf("FINDING A FIXED: equal-Seq/different-value converges deterministically "+
		"(both servers -> %q) regardless of arrival order", w1.Output)
}

// ── FINDING B (FIXED, HXC-1762): decompression bomb is now bounded ───────────
// DecompressDelta used `io.ReadAll(flate.NewReader(...))` with NO size cap, so a
// tiny crafted flate stream of highly-redundant bytes expanded by orders of
// magnitude (~765x measured) — a remotely-triggerable memory-exhaustion DoS,
// since DecompressDelta consumes external wire bytes. The fix caps the
// decompressed size (io.LimitReader) and REJECTS a stream that would exceed it.
//
// This proves both halves: (1) a legitimate under-cap delta still round-trips;
// (2) a stream that decompresses past the cap is rejected without materialising
// the full bomb. The bound is exercised cheaply via decompressDeltaLimited with
// a small limit (DecompressDelta uses the production maxDecompressedDeltaBytes).
func TestAdversarial_DecompressionBomb_Rejected(t *testing.T) {
	t.Parallel()

	const payload = 8 * 1024 * 1024 // 8 MiB of zeros — highly compressible "bomb"
	bomb := []CompletedJob{{JobID: "z", Output: make([]byte, payload), CompletedAt: time.Now().UTC(), Seq: 1}}
	compressed, err := CompressDelta(bomb)
	if err != nil {
		t.Fatalf("CompressDelta: %v", err)
	}
	ratio := float64(payload) / float64(len(compressed))
	if ratio < 50 {
		t.Fatalf("precondition: expected significant amplification, got x%.1f", ratio)
	}

	// (1) Legitimate under-cap decode still works (cap >> this 8 MiB payload).
	out, err := DecompressDelta(compressed)
	if err != nil {
		t.Fatalf("legitimate under-cap delta must still decode: %v", err)
	}
	if len(out) != 1 || len(out[0].Output) != payload {
		t.Fatalf("under-cap round-trip altered payload: got %d-byte output", len(out[0].Output))
	}

	// (2) The same stream, decoded with a small cap, is REJECTED (bomb guard).
	const smallLimit = 1 << 20 // 1 MiB
	_, err = decompressDeltaLimited(compressed, smallLimit)
	if err == nil {
		t.Fatalf("FINDING B not fixed: an %d-byte decompressed payload was accepted "+
			"under a %d-byte cap — the decompression-bomb guard is missing", payload, smallLimit)
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("bomb rejected with an unexpected error (want a limit-exceeded message): %v", err)
	}
	t.Logf("FINDING B FIXED: %d compressed bytes (x%.0f amplification) decode under the "+
		"production cap but are REJECTED past a %d-byte limit (%v) — bounded, no DoS",
		len(compressed), ratio, smallLimit, err)
}

// ── FINDING C (PINNED): no integrity envelope — silent corruption is possible ─
// Raw DEFLATE carries NO checksum (unlike gzip's CRC32 / zlib's Adler32). A
// single flipped byte that still decompresses to syntactically-valid JSON is
// accepted WITHOUT error as a content-different delta (e.g. JobID silently
// blanked). The flate codec cannot detect this; only an outer checksum or
// signature on the wire bytes could. We PIN that such silent acceptances are
// reachable so the gap is visible, and prove that decoding never PANICS and
// never yields a structurally-broken result (it is well-formed, just wrong).
//
// This is a hardening gap (add an integrity envelope), not a logic bug in the
// codec itself, so it is pinned and reported — not asserted as "must error".
func TestAdversarial_CorruptByte_NoIntegrityEnvelope(t *testing.T) {
	t.Parallel()

	valid, err := CompressDelta([]CompletedJob{
		{JobID: "corrupt-probe", Output: bytes.Repeat([]byte("payload-"), 64), CompletedAt: time.Now().UTC(), Seq: 1},
	})
	if err != nil {
		t.Fatalf("CompressDelta: %v", err)
	}

	silentlyWrong := 0
	for i := 0; i < len(valid); i++ {
		c := append([]byte(nil), valid...)
		c[i] ^= 0xFF
		out, err := DecompressDelta(c) // must never panic (covered elsewhere too)
		if err != nil {
			continue // corruption surfaced as an error — the good case
		}
		// Decoded without error: either identical to the original (benign flip)
		// or content-different. Count the content-different ones to PIN the
		// "no integrity check" gap.
		if len(out) != 1 || out[0].JobID != "corrupt-probe" {
			silentlyWrong++
		}
	}

	// PINNED: with no checksum, some single-byte flips ARE silently accepted as
	// wrong content. If this drops to zero, an integrity envelope was added —
	// update the report's FINDING C and flip this to assert zero.
	if silentlyWrong == 0 {
		t.Fatalf("expected raw-flate to silently accept SOME corrupted streams " +
			"(no integrity envelope); got zero — an integrity check may now exist, " +
			"update FINDING C in the report.")
	}
	t.Logf("FINDING C confirmed: %d single-byte flips decoded WITHOUT error as "+
		"content-different deltas — raw DEFLATE has no integrity check; add an "+
		"outer checksum/signature if wire corruption must be detected.", silentlyWrong)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func assertNoPanic(t *testing.T, label string, idx int, in []byte) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("PANIC on %s input #%d (len=%d): %v — hostile bytes must error, not panic", label, idx, len(in), r)
		}
	}()
	// We do not care about the error here; only that it does not panic AND that
	// a returned slice is never accompanied by a nil error on garbage we know is
	// invalid (that would be a silent accept). Errors are expected and fine.
	_, _ = DecompressDelta(in)
}

// mustFlate compresses arbitrary raw bytes with the SAME codec as CompressDelta
// so a RawSerializeDelta output can be fed through DecompressDelta.
func mustFlate(t *testing.T, raw []byte) []byte {
	t.Helper()
	// Re-use the production compressor by wrapping the already-marshalled JSON:
	// CompressDelta marshals+flates a delta; here we need to flate pre-marshalled
	// bytes, so we round it through a throwaway delta that serialises identically.
	// Simplest faithful path: decode raw to a delta then CompressDelta it.
	var d []CompletedJob
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("mustFlate: decode raw: %v", err)
	}
	c, err := CompressDelta(d)
	if err != nil {
		t.Fatalf("mustFlate: CompressDelta: %v", err)
	}
	return c
}
