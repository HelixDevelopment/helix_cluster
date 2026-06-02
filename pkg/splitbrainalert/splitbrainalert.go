// Package splitbrainalert codifies Prometheus alerting rules for split-brain
// and quorum-loss conditions in a Helix Cluster OS deployment (HXC-1380).
//
// PromQL expressions mirror the exact arithmetic used by pkg/splitbrain.Classify:
// reachable*2 compared against total avoids integer-division rounding bugs (e.g.
// floor(4/2)==2 would falsely grant majority to a 2-of-4 partition).
//
// The package provides a validated, round-trippable rule set and canonical YAML
// output suitable for import by Prometheus / Alertmanager without a live
// Prometheus instance: value is real parseable rule definitions, not fabricated
// fired-alert state.
package splitbrainalert

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"gopkg.in/yaml.v3"
)

// AlertRule is a single Prometheus alerting rule with runbook annotation.
type AlertRule struct {
	// Name is the Prometheus alert name (e.g. "HelixQuorumLoss").
	Name string
	// Expr is the PromQL expression that, when true for the For duration,
	// fires the alert. Static constants reflecting real split-brain math
	// (reachable*2 vs total — see pkg/splitbrain.Classify).
	Expr string
	// For is the Prometheus "for" duration string (e.g. "1m").
	// It must be parseable by time.ParseDuration.
	For string
	// Severity is the alert severity. Must be "critical" or "warning".
	Severity string
	// RunbookURL is a fully-qualified URL (scheme + host required) pointing
	// to the runbook for this alert.
	RunbookURL string
	// Summary is a short human-readable description used as the
	// annotations.summary value.
	Summary string
}

// validSeverities is the closed set of accepted severity values.
var validSeverities = map[string]bool{
	"critical": true,
	"warning":  true,
}

// Validate returns a non-nil error when any field of r is missing or invalid.
//
// Validation rules:
//   - Name must be non-empty.
//   - Expr must be non-empty.
//   - For must be parseable by time.ParseDuration, must not be empty, and must
//     be strictly positive (> 0). A zero duration (e.g. "0s" or "0") bypasses
//     the Prometheus debounce window entirely, which is operationally dangerous
//     for quorum-loss and isolation rules.
//   - Severity must be "critical" or "warning".
//   - RunbookURL must parse via url.Parse with a non-empty Scheme and Host.
//   - Summary must be non-empty.
func (r AlertRule) Validate() error {
	if r.Name == "" {
		return errors.New("splitbrainalert: Name must be non-empty")
	}
	if r.Expr == "" {
		return fmt.Errorf("splitbrainalert: rule %q: Expr must be non-empty", r.Name)
	}
	if r.For == "" {
		return fmt.Errorf("splitbrainalert: rule %q: For must be non-empty", r.Name)
	}
	d, err := time.ParseDuration(r.For)
	if err != nil {
		return fmt.Errorf("splitbrainalert: rule %q: For %q is not a valid duration: %w", r.Name, r.For, err)
	}
	if d <= 0 {
		return fmt.Errorf("splitbrainalert: rule %q: For must be a positive duration, got %q", r.Name, r.For)
	}
	if !validSeverities[r.Severity] {
		return fmt.Errorf("splitbrainalert: rule %q: Severity %q is not valid (must be critical or warning)", r.Name, r.Severity)
	}
	u, err := url.Parse(r.RunbookURL)
	if err != nil {
		return fmt.Errorf("splitbrainalert: rule %q: RunbookURL %q is not parseable: %w", r.Name, r.RunbookURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("splitbrainalert: rule %q: RunbookURL %q must have a non-empty scheme and host", r.Name, r.RunbookURL)
	}
	if r.Summary == "" {
		return fmt.Errorf("splitbrainalert: rule %q: Summary must be non-empty", r.Name)
	}
	return nil
}

// runbookBase is the base URL for Helix Cluster OS runbooks.
const runbookBase = "https://docs.helixcluster.io/runbooks"

// DefaultSplitBrainRules returns the canonical set of Prometheus alerting rules
// covering the three fundamental split-brain conditions, derived directly from
// the quorum arithmetic in pkg/splitbrain.Classify (2*reachable vs total):
//
//   - HelixQuorumLoss    — minority: 2*reachable < total (CRITICAL, 2 m pending)
//   - HelixTotalIsolation — complete isolation: reachable == 0 (CRITICAL, 30 s pending)
//   - HelixPartitionTie  — exact tie: 2*reachable == total (WARNING, 5 m pending)
//
// All returned rules pass Validate().
func DefaultSplitBrainRules() []AlertRule {
	return []AlertRule{
		{
			Name: "HelixQuorumLoss",
			// Fires when this partition sees strictly fewer than half of the
			// cluster members — the exact Minority condition in Classify.
			// Prometheus metric names follow the convention in pkg/swim and
			// pkg/federation: helix_members_reachable / helix_members_total.
			Expr:       "helix_members_reachable * 2 < helix_members_total",
			For:        "2m",
			Severity:   "critical",
			RunbookURL: runbookBase + "/split-brain/quorum-loss",
			Summary:    "Helix cluster has lost quorum: reachable*2 < total. Writes must be rejected.",
		},
		{
			Name: "HelixTotalIsolation",
			// Fires when the local node can reach zero peers — the
			// TotalIsolation condition in Classify (reachable == 0).
			Expr:       "helix_members_reachable == 0",
			For:        "30s",
			Severity:   "critical",
			RunbookURL: runbookBase + "/split-brain/total-isolation",
			Summary:    "Helix node is completely isolated: reachable == 0. All writes must stop immediately.",
		},
		{
			Name: "HelixPartitionTie",
			// Fires when the partition sees exactly half of all members —
			// the Tie condition in Classify (2*reachable == total).
			// Without a witness this grants no quorum; alert is WARNING to
			// prompt operator review before the situation degrades further.
			Expr:       "helix_members_reachable * 2 == helix_members_total",
			For:        "5m",
			Severity:   "warning",
			RunbookURL: runbookBase + "/split-brain/partition-tie",
			Summary:    "Helix cluster is in an even split: reachable*2 == total. Witness or tiebreaker required.",
		},
	}
}

// ---------------------------------------------------------------------------
// YAML structures — canonical Prometheus rule-group format.
// ---------------------------------------------------------------------------

// promRuleFile is the top-level Prometheus rules file structure.
type promRuleFile struct {
	Groups []promGroup `yaml:"groups"`
}

// promGroup is a single Prometheus rule group.
type promGroup struct {
	Name  string      `yaml:"name"`
	Rules []promRule  `yaml:"rules"`
}

// promRule is the YAML representation of one Prometheus alerting rule.
type promRule struct {
	Alert       string            `yaml:"alert"`
	Expr        string            `yaml:"expr"`
	For         string            `yaml:"for"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
}

// groupName is the rule-group name used in rendered YAML.
const groupName = "helix_split_brain"

// Render serialises rules into canonical Prometheus rule-group YAML.
//
// The output is suitable for direct use as a Prometheus rules file and
// contains, for each rule:
//
//	groups[].rules[].alert               — rule Name
//	groups[].rules[].expr                — PromQL Expr
//	groups[].rules[].for                 — For duration
//	groups[].rules[].labels.severity     — Severity
//	groups[].rules[].annotations.summary — Summary
//	groups[].rules[].annotations.runbook_url — RunbookURL
//
// Render returns an error when any rule fails Validate().
func Render(rules []AlertRule) ([]byte, error) {
	promRules := make([]promRule, 0, len(rules))
	for i, r := range rules {
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("splitbrainalert.Render: rule[%d]: %w", i, err)
		}
		promRules = append(promRules, promRule{
			Alert: r.Name,
			Expr:  r.Expr,
			For:   r.For,
			Labels: map[string]string{
				"severity": r.Severity,
			},
			Annotations: map[string]string{
				"summary":     r.Summary,
				"runbook_url": r.RunbookURL,
			},
		})
	}

	doc := promRuleFile{
		Groups: []promGroup{
			{
				Name:  groupName,
				Rules: promRules,
			},
		},
	}

	return yaml.Marshal(doc)
}

// Parse deserialises a Prometheus rule-group YAML document (as produced by
// Render) back into a slice of AlertRule values. It is the inverse of Render
// and guarantees a round-trip: Parse(Render(rules)) yields the same rule
// count, Names, and Exprs as the original slice.
//
// Parse reads the first group in the document. It does NOT call Validate on
// the resulting rules — callers that need validation must call it explicitly.
func Parse(doc []byte) ([]AlertRule, error) {
	var rf promRuleFile
	if err := yaml.Unmarshal(doc, &rf); err != nil {
		return nil, fmt.Errorf("splitbrainalert.Parse: YAML unmarshal failed: %w", err)
	}
	if len(rf.Groups) == 0 {
		return nil, errors.New("splitbrainalert.Parse: no rule groups found in document")
	}

	group := rf.Groups[0]
	out := make([]AlertRule, 0, len(group.Rules))
	for _, pr := range group.Rules {
		out = append(out, AlertRule{
			Name:       pr.Alert,
			Expr:       pr.Expr,
			For:        pr.For,
			Severity:   pr.Labels["severity"],
			RunbookURL: pr.Annotations["runbook_url"],
			Summary:    pr.Annotations["summary"],
		})
	}
	return out, nil
}
