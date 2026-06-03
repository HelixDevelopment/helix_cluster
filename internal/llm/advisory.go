package llm

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// AdvisoryKind classifies the type of action an advisory proposes. The kind is
// part of the persisted record so an operator can filter the advisory feed by
// the axis of the cluster it concerns.
type AdvisoryKind string

const (
	// KindMigration proposes moving a workload/replica between targets.
	KindMigration AdvisoryKind = "MIGRATION"
	// KindScaling proposes changing the replica/instance count of a target.
	KindScaling AdvisoryKind = "SCALING"
	// KindConfig proposes a configuration change on a target.
	KindConfig AdvisoryKind = "CONFIG"
	// KindAlert proposes raising an operator-facing alert.
	KindAlert AdvisoryKind = "ALERT"
	// KindOptimization proposes a non-urgent efficiency change.
	KindOptimization AdvisoryKind = "OPTIMIZATION"
)

// RiskLevel grades the blast radius of applying an advisory. Only LOW-risk
// advisories may be auto-applied; anything above LOW requires an explicit human
// approval before Apply will act (see Store.Apply).
type RiskLevel string

const (
	// RiskLow advisories may be auto-applied without approval.
	RiskLow RiskLevel = "LOW"
	// RiskMedium advisories require explicit approval.
	RiskMedium RiskLevel = "MEDIUM"
	// RiskHigh advisories require explicit approval.
	RiskHigh RiskLevel = "HIGH"
	// RiskCritical advisories require explicit approval.
	RiskCritical RiskLevel = "CRITICAL"
)

// AdvisoryState is the lifecycle state of a persisted advisory.
type AdvisoryState string

const (
	// StatePending is the state of every freshly generated advisory: persisted,
	// awaiting verification/approval, never yet applied.
	StatePending AdvisoryState = "PENDING"
	// StateApproved marks an advisory a human has explicitly approved for apply.
	StateApproved AdvisoryState = "APPROVED"
	// StateApplied marks an advisory that has been applied.
	StateApplied AdvisoryState = "APPLIED"
	// StateRejected marks an advisory the verifier (or an operator) rejected.
	StateRejected AdvisoryState = "REJECTED"
)

// Observation is the cluster signal handed to an Advisor to reason about. It is
// deliberately small and self-contained so the Advisor seam is deterministic
// and testable without any real telemetry pipeline.
type Observation struct {
	// Target is the entity the observation (and any resulting advisory) concerns
	// — e.g. a node id or service name. It MUST name a real target; the verifier
	// rejects advisories whose Target is not known to the cluster (hallucination).
	Target string
	// Signal is a short machine label for what was observed (e.g. "cpu_saturation").
	Signal string
	// Severity is a normalised [0,1] measure of how pressing the signal is.
	Severity float64
	// Detail carries free-form context the Advisor may fold into its rationale.
	Detail string
}

// Advisory is a single advisory-only recommendation produced by an Advisor and
// persisted by the Store. It is NEVER binding on its own: a non-LOW-risk
// Advisory cannot be applied without an explicit approval, and every Advisory
// must clear the LLMsVerifier gate before it is eligible to be applied.
type Advisory struct {
	// ID is assigned by the Store on persistence; an Advisor leaves it empty.
	ID string
	// Kind classifies the proposed action.
	Kind AdvisoryKind
	// Target is the entity the action applies to (mirrors Observation.Target).
	Target string
	// Action is a short imperative description of what is proposed.
	Action string
	// Rationale is the chain-of-thought string explaining WHY the advisory was
	// produced. It is persisted verbatim so an operator can audit the reasoning.
	Rationale string
	// Confidence is the Advisor's self-reported certainty in [0,1].
	Confidence float64
	// RiskLevel grades the blast radius (drives the approval gate).
	RiskLevel RiskLevel
	// State is the lifecycle state; the Store owns transitions.
	State AdvisoryState
	// CreatedAt is set by the Store when the advisory is persisted.
	CreatedAt time.Time
}

// clone returns a value copy of the advisory so callers of Get/List cannot
// mutate the Store's internal record through a shared pointer.
func (a *Advisory) clone() *Advisory {
	cp := *a
	return &cp
}

// Advisor is the injectable LLM seam. A real Advisor calls out to an LLM to
// reason about an Observation; tests inject a deterministic in-repo Advisor so
// the engine is exercised end-to-end WITHOUT any network or real model. An
// Advisor proposes only — it never persists, approves, or applies.
type Advisor interface {
	Propose(ctx context.Context, obs Observation) (Advisory, error)
}

// ErrApprovalRequired is returned by Apply when a non-LOW-risk advisory is
// applied without a prior explicit approval. It is typed so callers can
// distinguish "needs a human" from a genuine failure.
type ErrApprovalRequired struct {
	ID   string
	Risk RiskLevel
}

func (e *ErrApprovalRequired) Error() string {
	return fmt.Sprintf("llm: advisory %s has risk %s and requires explicit approval before apply", e.ID, e.Risk)
}

// IsApprovalRequired reports whether err is (or wraps) ErrApprovalRequired.
func IsApprovalRequired(err error) bool {
	var t *ErrApprovalRequired
	return errors.As(err, &t)
}

// ErrAdvisoryNotFound is returned for an operation on an unknown advisory id.
type ErrAdvisoryNotFound struct {
	ID string
}

func (e *ErrAdvisoryNotFound) Error() string {
	return fmt.Sprintf("llm: advisory not found: %s", e.ID)
}

// IsAdvisoryNotFound reports whether err is (or wraps) ErrAdvisoryNotFound.
func IsAdvisoryNotFound(err error) bool {
	var t *ErrAdvisoryNotFound
	return errors.As(err, &t)
}

// Store persists generated advisories and owns their lifecycle. It is an
// in-memory record store: every generated advisory is persisted in StatePending
// and exposed via Get/List. The Store enforces the two binding guarantees:
//   - a non-LOW-risk advisory is never auto-applied without an explicit Approve;
//   - every advisory must clear the LLMsVerifier gate before Generate persists
//     it as PENDING (a rejected advisory is persisted as REJECTED, never applied).
type Store struct {
	mu       sync.Mutex
	advisor  Advisor
	verifier *Verifier
	records  map[string]*Advisory
	seq      int
	now      func() time.Time
}

// NewStore creates an advisory Store backed by the given Advisor (LLM seam) and
// Verifier (constitutional gate). Both are mandatory: the verifier is the
// non-optional safety gate per the advisory engine's contract.
func NewStore(advisor Advisor, verifier *Verifier) *Store {
	return &Store{
		advisor:  advisor,
		verifier: verifier,
		records:  make(map[string]*Advisory),
		now:      time.Now,
	}
}

// Generate runs the Advisor against obs, gates the proposal through the
// mandatory Verifier, and persists the result. A proposal that fails the
// verifier is persisted in StateRejected and returned WITH the typed verifier
// error so the caller has both the record and the reason; a proposal that
// passes is persisted in StatePending. The persisted advisory (with its
// assigned ID, CreatedAt, rationale, confidence and risk) is always returned.
func (s *Store) Generate(ctx context.Context, obs Observation) (*Advisory, error) {
	proposed, err := s.advisor.Propose(ctx, obs)
	if err != nil {
		return nil, fmt.Errorf("llm: advisor propose failed: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	id := fmt.Sprintf("adv-%d", s.seq)
	rec := proposed.clone()
	rec.ID = id
	rec.CreatedAt = s.now()

	// Mandatory constitutional gate. A hallucinated/unsafe action is persisted
	// as REJECTED (for audit) and never becomes eligible for apply.
	if verr := s.verifier.Verify(rec); verr != nil {
		rec.State = StateRejected
		s.records[id] = rec
		return rec.clone(), verr
	}

	rec.State = StatePending
	s.records[id] = rec
	return rec.clone(), nil
}

// Get returns a snapshot copy of the advisory with the given id.
func (s *Store) Get(id string) (*Advisory, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return nil, false
	}
	return rec.clone(), true
}

// List returns snapshot copies of all persisted advisories, ordered by ID
// (ascending) so the feed is deterministic.
func (s *Store) List() []*Advisory {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Advisory, 0, len(s.records))
	for _, rec := range s.records {
		out = append(out, rec.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Approve records an explicit human approval for a non-LOW-risk advisory,
// transitioning it from PENDING to APPROVED so Apply may act on it. Approving an
// already-rejected or already-applied advisory is refused.
func (s *Store) Approve(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return &ErrAdvisoryNotFound{ID: id}
	}
	if rec.State != StatePending {
		return fmt.Errorf("llm: advisory %s is %s, only PENDING advisories may be approved", id, rec.State)
	}
	rec.State = StateApproved
	return nil
}

// Apply applies a persisted advisory and records it as APPLIED. The binding
// guarantee is enforced here: a non-LOW-risk advisory is rejected with
// ErrApprovalRequired unless it has been explicitly Approved first; a LOW-risk
// advisory may be auto-applied directly from PENDING. A rejected advisory is
// never applicable. Apply returns the post-apply record.
func (s *Store) Apply(id string) (*Advisory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return nil, &ErrAdvisoryNotFound{ID: id}
	}
	switch rec.State {
	case StateApplied:
		return nil, fmt.Errorf("llm: advisory %s already applied", id)
	case StateRejected:
		return nil, fmt.Errorf("llm: advisory %s was rejected by the verifier and is not applicable", id)
	}
	// The core advisory-only guard: anything above LOW risk must carry an
	// explicit approval (StateApproved). A non-LOW advisory still in PENDING is
	// never auto-applied.
	if rec.RiskLevel != RiskLow && rec.State != StateApproved {
		return nil, &ErrApprovalRequired{ID: id, Risk: rec.RiskLevel}
	}
	rec.State = StateApplied
	return rec.clone(), nil
}
