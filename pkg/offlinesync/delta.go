package offlinesync

import (
	"bytes"
	"compress/flate"
	"encoding/json"
	"fmt"
	"io"
)

// CompressDelta serializes delta to JSON and compresses the result with
// compress/flate (BestCompression level).  The output is a self-contained
// byte slice that can be safely passed to DecompressDelta.
//
// For payloads with repeated structure (the common case: many jobs sharing the
// same key names / output patterns), the flate-compressed form is strictly
// smaller than the raw JSON serialisation.
func CompressDelta(delta []CompletedJob) ([]byte, error) {
	raw, err := json.Marshal(delta)
	if err != nil {
		return nil, fmt.Errorf("offlinesync: marshal delta: %w", err)
	}
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("offlinesync: create flate writer: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return nil, fmt.Errorf("offlinesync: flate write: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("offlinesync: flate close: %w", err)
	}
	return buf.Bytes(), nil
}

// maxDecompressedDeltaBytes bounds how large a decompressed delta may be. Raw
// DEFLATE (compress/flate) carries no uncompressed-size header, so a tiny
// crafted stream of highly-redundant bytes expands without limit — a
// decompression bomb. Because DecompressDelta consumes EXTERNAL wire bytes, an
// unbounded io.ReadAll on the flate stream is a remotely-triggerable
// memory-exhaustion DoS (measured ~765x amplification: ~11 KB -> 8 MiB, scaling
// linearly to multi-GB). The cap is far above any realistic offline-sync delta;
// a stream that would exceed it is treated as hostile and rejected rather than
// materialised. Tune via this constant if a deployment legitimately needs more.
const maxDecompressedDeltaBytes = 256 << 20 // 256 MiB

// DecompressDelta is the inverse of CompressDelta: it decompresses the flate
// stream and unmarshals the JSON back into a []CompletedJob.  The round-trip
// is lossless for all fields including binary Output.
//
// The decompressed size is bounded by maxDecompressedDeltaBytes; a stream that
// would expand beyond the cap is rejected (decompression-bomb guard) instead of
// being allocated, so a hostile input cannot exhaust memory.
func DecompressDelta(compressed []byte) ([]CompletedJob, error) {
	return decompressDeltaLimited(compressed, maxDecompressedDeltaBytes)
}

// decompressDeltaLimited is DecompressDelta with an explicit decompressed-size
// bound, so the bomb-guard can be exercised cheaply in tests with a small limit
// (DecompressDelta always passes the production maxDecompressedDeltaBytes).
func decompressDeltaLimited(compressed []byte, limit int64) ([]CompletedJob, error) {
	r := flate.NewReader(bytes.NewReader(compressed))
	defer r.Close()
	// Read at most limit+1 decompressed bytes: io.LimitReader stops consuming the
	// flate output at the bound, so a bomb never materialises beyond limit+1, and
	// crossing the cap is detected and rejected below.
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("offlinesync: flate read: %w", err)
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("offlinesync: decompressed delta exceeds %d-byte limit (possible decompression bomb)", limit)
	}
	var jobs []CompletedJob
	if err := json.Unmarshal(raw, &jobs); err != nil {
		return nil, fmt.Errorf("offlinesync: unmarshal delta: %w", err)
	}
	return jobs, nil
}

// RawSerializeDelta returns the uncompressed JSON serialisation of delta.
// It is used by tests to measure the compression ratio.
func RawSerializeDelta(delta []CompletedJob) ([]byte, error) {
	return json.Marshal(delta)
}
