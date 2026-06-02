// Package auditproof implements a commit-then-prove reproducible audit.
//
// Model: an audit report is computed from per-node compute-unit data. A
// SHA-256 commitment is taken over the canonical serialization of the
// per-node incentives that are DERIVED from that data. A verifier
// independently recomputes the incentives from the same compute-unit data,
// recomputes the commitment, and confirms it MATCHES the original commitment
// (proving the report was not tampered). If the underlying data is altered,
// the recomputed commitment will not match, so tampering is detected.
//
// The package is pure Go (stdlib crypto/sha256 only), deterministic, and
// performs no network/host/cgo access. The canonical serialization sorts
// node identifiers so the commitment is independent of Go map iteration
// order — a non-canonical (map-order-dependent) serialization would yield a
// non-deterministic commitment.
package auditproof

import (
	"crypto/sha256"
	"math"
	"sort"
	"strconv"
)

// Report is the audit input: per-node compute-unit measurements.
//
// ComputeUnits maps a node identifier to the number of compute units that
// node contributed during the audited window. Incentives are derived from
// these values; nothing about the incentive is stored here.
type Report struct {
	ComputeUnits map[string]float64
}

// incentiveRatePerUnit is the deterministic conversion factor from a node's
// compute-unit count to its incentive. It is a fixed constant of the audit
// model so that incentives are a pure, reproducible function of the data.
const incentiveRatePerUnit = 1.25

// ComputeIncentives derives the per-node incentive map from the report's
// compute-unit data. The incentive for a node is its compute-unit count
// scaled by incentiveRatePerUnit. The result is purely a function of the
// input data; no value is hardcoded per node.
//
// A nil ComputeUnits map yields an empty (non-nil) incentive map.
func ComputeIncentives(data Report) map[string]float64 {
	incentives := make(map[string]float64, len(data.ComputeUnits))
	for node, units := range data.ComputeUnits {
		incentives[node] = units * incentiveRatePerUnit
	}
	return incentives
}

// canonicalSerialize produces a deterministic byte serialization of an
// incentive map. Node identifiers are sorted so the output is independent of
// Go map iteration order. Each entry is encoded length-prefixed so that no
// pair of distinct maps can collide on the byte stream:
//
//	for each node (in sorted order):
//	    len(node) "\x1f" node "\x1f" canonicalFloat(incentive) "\x1e"
//
// canonicalFloat uses strconv with a fixed format so a given float64 always
// serializes identically.
func canonicalSerialize(incentives map[string]float64) []byte {
	nodes := make([]string, 0, len(incentives))
	for node := range incentives {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)

	var buf []byte
	for _, node := range nodes {
		// Length-prefix the node id to make the encoding injective.
		buf = strconv.AppendInt(buf, int64(len(node)), 10)
		buf = append(buf, 0x1f) // unit separator
		buf = append(buf, node...)
		buf = append(buf, 0x1f)
		buf = append(buf, canonicalFloat(incentives[node])...)
		buf = append(buf, 0x1e) // record separator
	}
	return buf
}

// canonicalFloat returns a deterministic textual encoding of f. It uses a
// fixed-precision 'g' format with full float64 precision so equal values map
// to equal strings across runs and platforms.
func canonicalFloat(f float64) string {
	// Normalize negative zero so it serializes identically to positive zero.
	if f == 0 {
		f = 0
	}
	return strconv.FormatFloat(f, 'g', 17, 64)
}

// Commit returns the SHA-256 commitment over the canonical serialization of
// the incentive map. The same incentive map always yields the same
// commitment (determinism), regardless of map iteration order.
func Commit(incentives map[string]float64) [32]byte {
	return sha256.Sum256(canonicalSerialize(incentives))
}

// CommitReport is a convenience that derives incentives from the report and
// commits to them in one step. The returned commitment is what an honest
// auditor would publish alongside the report.
func CommitReport(data Report) [32]byte {
	return Commit(ComputeIncentives(data))
}

// Verify independently recomputes the incentives from data, recomputes the
// commitment, and reports whether it MATCHES the supplied commitment.
//
// It returns true only when the recomputed commitment equals commitment byte
// for byte. If the underlying data has been altered relative to what produced
// commitment, the recomputed commitment differs and Verify returns false
// (tamper detected). Verify never trusts the supplied commitment as an
// oracle: the answer is a function of data, not of commitment.
func Verify(data Report, commitment [32]byte) bool {
	recomputed := CommitReport(data)
	return recomputed == commitment
}

// approxEqual is a small helper for callers/tests that need to compare
// derived floating-point incentives without relying on exact equality.
func approxEqual(a, b float64) bool {
	const eps = 1e-9
	return math.Abs(a-b) <= eps
}

var _ = approxEqual
