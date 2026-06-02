// Package compliancedoc implements the EU AI Act compliance documentation
// pipeline (HXC-1481). It turns a set of real GPU attestation-log entries plus
// model metadata into a deterministic, provenance-bound model card document.
//
// The pipeline is fully self-contained and uses only the Go standard library.
// The ProvenanceHash is a SHA-256 over a deterministic canonical serialization
// of the sorted attestation records, so it is cryptographically bound to the
// actual content of every log entry: any change to a record's GPU UUID, model
// hash, pass/fail outcome, or timestamp changes the hash. This satisfies the
// EU AI Act requirement that the technical documentation provide a tamper-
// evident provenance anchor for the attestation evidence.
package compliancedoc

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrNoAttestations is returned by Generate when no attestation records are
// supplied. A compliance model card cannot be produced without evidence.
var ErrNoAttestations = errors.New("compliancedoc: no attestation records")

// AttestationRecord is one real attestation-log entry produced by a GPU
// attestation run.
type AttestationRecord struct {
	Timestamp time.Time
	GPUUUID   string
	ModelHash string
	Passed    bool
}

// ModelMetadata describes the model the compliance documentation is about.
type ModelMetadata struct {
	Name        string
	Version     string
	Provider    string
	IntendedUse string
}

// ModelCard is the generated EU AI Act compliance model card.
type ModelCard struct {
	Metadata         ModelMetadata
	AttestationCount int
	PassedCount      int
	ProvenanceHash   string
	GeneratedAt      time.Time
	Document         string
}

// canonicalSerialize produces a deterministic byte serialization of the sorted
// attestation records. Records are sorted by a stable composite key so that the
// ordering of the input slice does not affect the result, while the content of
// every field still contributes to the bytes that are hashed.
func canonicalSerialize(logs []AttestationRecord) []byte {
	sorted := make([]AttestationRecord, len(logs))
	copy(sorted, logs)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if !a.Timestamp.Equal(b.Timestamp) {
			return a.Timestamp.Before(b.Timestamp)
		}
		if a.GPUUUID != b.GPUUUID {
			return a.GPUUUID < b.GPUUUID
		}
		if a.ModelHash != b.ModelHash {
			return a.ModelHash < b.ModelHash
		}
		return boolRank(a.Passed) < boolRank(b.Passed)
	})

	var sb strings.Builder
	for _, r := range sorted {
		// UnixNano in a fixed-radix form keeps the timestamp content-bound and
		// independent of locale/formatting.
		fmt.Fprintf(&sb, "%d\x1f%s\x1f%s\x1f%t\x1e",
			r.Timestamp.UTC().UnixNano(), r.GPUUUID, r.ModelHash, r.Passed)
	}
	return []byte(sb.String())
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// distinctGPUUUIDs returns the sorted set of distinct GPU UUIDs in the logs.
func distinctGPUUUIDs(logs []AttestationRecord) []string {
	seen := make(map[string]struct{}, len(logs))
	var out []string
	for _, r := range logs {
		if _, ok := seen[r.GPUUUID]; ok {
			continue
		}
		seen[r.GPUUUID] = struct{}{}
		out = append(out, r.GPUUUID)
	}
	sort.Strings(out)
	return out
}

// Generate builds an EU AI Act compliance model card from the model metadata
// and the supplied attestation log. It returns ErrNoAttestations if logs is
// empty. The ProvenanceHash is bound to the actual content of every record.
func Generate(meta ModelMetadata, logs []AttestationRecord, now time.Time) (ModelCard, error) {
	if len(logs) == 0 {
		return ModelCard{}, ErrNoAttestations
	}

	sum := sha256.Sum256(canonicalSerialize(logs))
	provenance := hex.EncodeToString(sum[:])

	passed := 0
	for _, r := range logs {
		if r.Passed {
			passed++
		}
	}

	gpus := distinctGPUUUIDs(logs)

	doc := renderDocument(meta, len(logs), passed, gpus, provenance, now)

	return ModelCard{
		Metadata:         meta,
		AttestationCount: len(logs),
		PassedCount:      passed,
		ProvenanceHash:   provenance,
		GeneratedAt:      now,
		Document:         doc,
	}, nil
}

// renderDocument produces the markdown model-card text.
func renderDocument(meta ModelMetadata, count, passed int, gpus []string, provenance string, now time.Time) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "# EU AI Act Model Card: %s\n\n", meta.Name)
	fmt.Fprintf(&sb, "- **Model:** %s\n", meta.Name)
	fmt.Fprintf(&sb, "- **Version:** %s\n", meta.Version)
	fmt.Fprintf(&sb, "- **Provider:** %s\n", meta.Provider)
	fmt.Fprintf(&sb, "- **Intended use:** %s\n", meta.IntendedUse)
	fmt.Fprintf(&sb, "- **Generated at:** %s\n\n", now.UTC().Format(time.RFC3339Nano))

	sb.WriteString("## Attestation Summary\n\n")
	fmt.Fprintf(&sb, "- Attestation count: %d\n", count)
	fmt.Fprintf(&sb, "- Passed count: %d\n", passed)
	fmt.Fprintf(&sb, "- Failed count: %d\n\n", count-passed)

	sb.WriteString("## Attesting GPUs\n\n")
	for _, g := range gpus {
		fmt.Fprintf(&sb, "- %s\n", g)
	}
	sb.WriteString("\n")

	sb.WriteString("## EU AI Act Technical Documentation\n\n")
	sb.WriteString("This section anchors the provenance of the attestation evidence ")
	sb.WriteString("recorded above. The provenance hash is a SHA-256 digest over a ")
	sb.WriteString("deterministic canonical serialization of all attestation records; ")
	sb.WriteString("any modification to the evidence changes this value.\n\n")
	fmt.Fprintf(&sb, "- **Provenance hash (SHA-256):** %s\n", provenance)

	return sb.String()
}
