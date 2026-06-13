package hxcregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// HXCItem represents a single workable item in the Helix Cluster OS registry.
type HXCItem struct {
	HXCID           string    `json:"hxc_id"`
	Type            string    `json:"type"`
	Status          string    `json:"status"`
	Priority        string    `json:"priority"`
	Phase           int       `json:"phase"`
	Title           string    `json:"title"`
	Description     string    `json:"description"`
	CommitSHA       string    `json:"commit_sha,omitempty"`
	ForensicAnchor  string    `json:"forensic_anchor,omitempty"`
	ClosureCriteria string    `json:"closure_criteria,omitempty"`
	ComposesWith    string    `json:"composes_with,omitempty"` // JSON array
	CurrentLocation string    `json:"current_location"`
	HeadingHash     string    `json:"heading_hash"`
	CreatedAt       time.Time `json:"created_at"`
	LastModified    time.Time `json:"last_modified"`
}

// Validate checks that the item meets minimum constraints.
func (i *HXCItem) Validate() error {
	if i.HXCID == "" {
		return fmt.Errorf("hxc_id is required")
	}
	if i.Title == "" {
		return fmt.Errorf("title is required")
	}
	if i.Description == "" {
		return fmt.Errorf("description is required")
	}
	validTypes := map[string]bool{"Bug": true, "Feature": true, "Task": true, "Research": true, "Docs": true}
	if !validTypes[i.Type] {
		return fmt.Errorf("invalid type: %s", i.Type)
	}
	validStatuses := map[string]bool{"Queued": true, "In progress": true, "Ready for testing": true, "In testing": true, "Completed": true, "Obsolete": true}
	if !validStatuses[i.Status] {
		return fmt.Errorf("invalid status: %s", i.Status)
	}
	validPriorities := map[string]bool{"P0": true, "P1": true, "P2": true, "P3": true}
	if !validPriorities[i.Priority] {
		return fmt.Errorf("invalid priority: %s", i.Priority)
	}
	return nil
}

// Canonical document locations. The renderer (scripts/docs/db_to_md.py) splits
// items into docs/issues.md vs docs/fixed.md purely by current_location, NOT by
// status, so location MUST track status or completed work silently under-reports
// in fixed.md (a CLAUDE-1 / §11.4.93 doc-sync truthfulness defect).
const (
	LocationIssues = "Issues"
	LocationFixed  = "Fixed"
)

// terminalStatuses are the end-state statuses whose items belong in the "Fixed"
// document location. An item in any of these is done (Completed) or closed
// won't-do (Obsolete); either way it is no longer an active issue, so the
// renderer must place it in fixed.md, not issues.md.
var terminalStatuses = map[string]bool{
	"Completed": true,
	"Obsolete":  true,
}

// IsTerminalStatus reports whether status is an end-state status that maps to
// the "Fixed" document location.
func IsTerminalStatus(status string) bool {
	return terminalStatuses[status]
}

// CanonicalLocation returns the document location an item with the given status
// MUST live in for the renderer to file it correctly: "Fixed" for terminal
// (done/obsolete) statuses, "Issues" for every active status. This is the single
// source of the status->location mapping used by both the update path and the
// reconcile path.
func CanonicalLocation(status string) string {
	if IsTerminalStatus(status) {
		return LocationFixed
	}
	return LocationIssues
}

// ComputeHeadingHash generates a stable hash from the title for re-sync binding.
func (i *HXCItem) ComputeHeadingHash() string {
	h := sha256.New()
	h.Write([]byte(i.Title))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// EnsureHash sets HeadingHash if empty.
func (i *HXCItem) EnsureHash() {
	if i.HeadingHash == "" {
		i.HeadingHash = i.ComputeHeadingHash()
	}
}
