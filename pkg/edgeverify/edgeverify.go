// Package edgeverify implements redundant edge-output verification against a
// single TRUSTED anchor (pure Go, deterministic).
//
// Model: a work unit is executed REDUNDANTLY on several edge devices. One
// device is the TRUSTED reference (or its result is taken as the trusted
// baseline). A Verifier computes a content checksum (SHA-256) of each device's
// result payload and compares every peer's checksum against the trusted
// checksum. A MATCH is accepted; a MISMATCH is detected and the offending
// device's result is REJECTED/FLAGGED. The Report names exactly which devices
// disagreed with the trusted anchor.
//
// This is DISTINCT from package redundantexec, which is a BOINC-style quorum
// trust-scorer (majority agreement among peers, reputation deltas). edgeverify
// has NO quorum and NO trust score: correctness is defined purely by byte-exact
// agreement with the one trusted reference, using a content checksum (so a
// single differing byte is detected — not a length-only comparison).
//
// The package is pure Go, deterministic, and self-contained: it performs no
// network/host I/O and consults no clock. The same inputs always produce the
// same Report.
package edgeverify

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// Result is one device's output for a redundant work unit. Payload is the raw
// result bytes; verification is performed on a content checksum of Payload, so
// two devices agree iff their payloads are byte-for-byte identical.
type Result struct {
	DeviceID string
	Payload  []byte
}

// PeerVerdict records the verification outcome for a single peer device against
// the trusted anchor.
type PeerVerdict struct {
	DeviceID string
	// Checksum is the hex SHA-256 of this peer's payload.
	Checksum string
	// TrustedChecksum is the hex SHA-256 of the trusted anchor's payload
	// (repeated here so a verdict is self-describing).
	TrustedChecksum string
	// OK is true iff Checksum == TrustedChecksum (peer agreed with the anchor).
	OK bool
}

// Report is the overall verification outcome for a work unit. TrustedDevice and
// TrustedChecksum identify the anchor. Peers holds one verdict per peer (in the
// order peers were supplied). Mismatched lists, sorted, the IDs of every peer
// that DISAGREED with the anchor. Accepted is true iff every peer matched.
type Report struct {
	TrustedDevice   string
	TrustedChecksum string
	Peers           []PeerVerdict
	Mismatched      []string
	Accepted        bool
}

// Checksum returns the hex-encoded SHA-256 content checksum of payload. This is
// the verification primitive: it depends on every byte of payload, so a
// single-byte change yields a different checksum (unlike a length comparison).
func Checksum(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Verify checks each peer result against the trusted reference by comparing
// content checksums. A peer whose checksum matches the trusted checksum is
// marked OK; a peer whose checksum differs is flagged as a MISMATCH and its
// device ID is added to Report.Mismatched. The overall verdict is Accepted iff
// no peer mismatched.
//
// The trusted result itself is the anchor and is NOT included in Peers; if it
// happens to also appear in peers it is verified like any other peer (and will
// match itself).
func Verify(trusted Result, peers []Result) Report {
	trustedSum := Checksum(trusted.Payload)

	rep := Report{
		TrustedDevice:   trusted.DeviceID,
		TrustedChecksum: trustedSum,
		Peers:           make([]PeerVerdict, 0, len(peers)),
		Accepted:        true,
	}

	for _, p := range peers {
		sum := Checksum(p.Payload)
		ok := sum == trustedSum
		rep.Peers = append(rep.Peers, PeerVerdict{
			DeviceID:        p.DeviceID,
			Checksum:        sum,
			TrustedChecksum: trustedSum,
			OK:              ok,
		})
		if !ok {
			rep.Mismatched = append(rep.Mismatched, p.DeviceID)
			rep.Accepted = false
		}
	}

	sort.Strings(rep.Mismatched)
	return rep
}
