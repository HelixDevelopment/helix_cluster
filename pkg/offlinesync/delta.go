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

// DecompressDelta is the inverse of CompressDelta: it decompresses the flate
// stream and unmarshals the JSON back into a []CompletedJob.  The round-trip
// is lossless for all fields including binary Output.
func DecompressDelta(compressed []byte) ([]CompletedJob, error) {
	r := flate.NewReader(bytes.NewReader(compressed))
	defer r.Close()
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("offlinesync: flate read: %w", err)
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
