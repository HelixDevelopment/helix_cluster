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
