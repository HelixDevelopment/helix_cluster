// Package failconfirm implements a Redis-cluster-style two-phase failure
// confirmation protocol: a peer is first marked PFAIL (possible failure /
// Suspect) when a single node times it out locally, and is promoted to FAIL
// (Dead) only once a MAJORITY (quorum = floor(N/2)+1) of DISTINCT cluster
// members independently report PFAIL for it.
//
// This is the majority-confirmation protocol, deliberately distinct from
// phi-accrual suspicion (which is a single-observer statistical estimate).
// Here promotion to FAIL is a cluster-wide quorum decision.
//
// The package is fully self-contained and deterministic: it defines its own
// minimal node/quorum model, performs no I/O, and uses no wall-clock time in
// its logic. An optional Clock can be injected to enforce a TTL window on
// suspicions; without one, suspicions never expire on their own.
package failconfirm

import (
	"errors"
	"sort"
)

// State is the confirmation lifecycle state of a target peer.
type State int

const (
	// Alive means no outstanding (non-expired) PFAIL reports for the target.
	Alive State = iota
	// Suspect means at least one but fewer than quorum distinct reporters
	// currently hold a PFAIL report for the target (Redis PFAIL).
	Suspect
	// Dead means a quorum of distinct reporters confirmed PFAIL: the target
	// has been promoted to FAIL. This is terminal.
	Dead
)

// String renders the state for logs and t.Logf evidence.
func (s State) String() string {
	switch s {
	case Alive:
		return "Alive"
	case Suspect:
		return "Suspect"
	case Dead:
		return "Dead"
	default:
		return "Unknown"
	}
}

// NodeID identifies a cluster member (reporter or target).
type NodeID string

// Clock supplies a monotonically non-decreasing logical time. It is injected so
// that tests and callers control TTL expiry deterministically — never wall
// clock. A nil Clock disables TTL-based expiry entirely.
type Clock interface {
	// Now returns the current logical tick.
	Now() int64
}

// Errors returned by Detector construction and operations.
var (
	// ErrTooFewMembers is returned when fewer than 1 member is provided.
	ErrTooFewMembers = errors.New("failconfirm: at least one member required")
	// ErrUnknownNode is returned when a reporter or target is not a member.
	ErrUnknownNode = errors.New("failconfirm: node is not a cluster member")
)

// suspicion records, per target, the set of distinct reporters and the logical
// tick at which each reporter last (re)asserted PFAIL — used for TTL expiry.
type suspicion struct {
	// reporters maps reporter -> last-asserted tick. The KEY SET is what the
	// quorum is computed over: a reporter present once counts once regardless
	// of how many times it (re)reports.
	reporters map[NodeID]int64
}

// Detector tracks PFAIL reports across N members and promotes a target to FAIL
// (Dead) once a quorum of DISTINCT members report PFAIL within the TTL window.
//
// Detector is not safe for concurrent use; callers must serialize access. This
// is intentional for deterministic, single-goroutine simulation/testing.
type Detector struct {
	members map[NodeID]struct{}
	quorum  int
	clock   Clock
	ttl     int64 // logical ticks; <=0 or nil clock means no expiry
	onDead  func(NodeID)
	susp    map[NodeID]*suspicion
	dead    map[NodeID]struct{} // targets already promoted (fired exactly once)
}

// Option configures a Detector.
type Option func(*Detector)

// WithClock injects a logical Clock enabling TTL-based suspicion expiry.
func WithClock(c Clock) Option {
	return func(d *Detector) { d.clock = c }
}

// WithTTL sets the suspicion TTL in logical ticks. A PFAIL report older than
// ttl (relative to clock.Now()) no longer counts toward quorum. Requires a
// Clock; without one the TTL is ignored.
func WithTTL(ttl int64) Option {
	return func(d *Detector) { d.ttl = ttl }
}

// OnDead registers a callback fired EXACTLY ONCE when a target is promoted to
// FAIL. Subsequent reports for an already-dead target never fire it again.
func OnDead(cb func(NodeID)) Option {
	return func(d *Detector) { d.onDead = cb }
}

// New constructs a Detector over the given distinct members. The quorum is
// computed as floor(N/2)+1 (strict majority). Duplicate member IDs are
// collapsed. Returns ErrTooFewMembers if no members are supplied.
func New(members []NodeID, opts ...Option) (*Detector, error) {
	set := make(map[NodeID]struct{}, len(members))
	for _, m := range members {
		set[m] = struct{}{}
	}
	if len(set) == 0 {
		return nil, ErrTooFewMembers
	}
	d := &Detector{
		members: set,
		quorum:  len(set)/2 + 1,
		susp:    make(map[NodeID]*suspicion),
		dead:    make(map[NodeID]struct{}),
	}
	for _, o := range opts {
		o(d)
	}
	return d, nil
}

// Quorum returns the number of distinct reporters required to promote a target
// to FAIL.
func (d *Detector) Quorum() int { return d.quorum }

// Members returns the sorted member IDs (stable for deterministic logging).
func (d *Detector) Members() []NodeID {
	out := make([]NodeID, 0, len(d.members))
	for m := range d.members {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (d *Detector) isMember(n NodeID) bool {
	_, ok := d.members[n]
	return ok
}

// now returns the current logical tick, or 0 when no clock is configured.
func (d *Detector) now() int64 {
	if d.clock == nil {
		return 0
	}
	return d.clock.Now()
}

// ttlActive reports whether TTL expiry is in effect.
func (d *Detector) ttlActive() bool {
	return d.clock != nil && d.ttl > 0
}

// liveReporterCount returns the number of distinct, non-expired reporters for a
// target, pruning expired entries in place.
func (d *Detector) liveReporterCount(target NodeID) int {
	s := d.susp[target]
	if s == nil {
		return 0
	}
	if d.ttlActive() {
		cutoff := d.now() - d.ttl
		for r, at := range s.reporters {
			if at <= cutoff {
				delete(s.reporters, r)
			}
		}
	}
	return len(s.reporters)
}

// ReportPFAIL records that reporter locally suspects target (PFAIL). The same
// reporter reporting multiple times counts as ONE distinct suspicion; a repeat
// report merely refreshes its TTL tick. If this report makes the distinct,
// non-expired reporter count reach quorum, the target is promoted to FAIL and
// the OnDead callback fires exactly once.
//
// Reports for an already-Dead target are ignored (FAIL is terminal) and never
// re-fire OnDead. Both reporter and target must be members.
func (d *Detector) ReportPFAIL(reporter, target NodeID) error {
	if !d.isMember(reporter) || !d.isMember(target) {
		return ErrUnknownNode
	}
	if _, dead := d.dead[target]; dead {
		return nil
	}
	s := d.susp[target]
	if s == nil {
		s = &suspicion{reporters: make(map[NodeID]int64)}
		d.susp[target] = s
	}
	// Distinct-reporter semantics: keyed by reporter, so a second report from
	// the same reporter overwrites its tick rather than adding a new vote.
	s.reporters[reporter] = d.now()

	if d.liveReporterCount(target) >= d.quorum {
		d.promote(target)
	}
	return nil
}

// ClearPFAIL retracts reporter's PFAIL for target (refutation, e.g. the target
// responded to that reporter). If the retraction drops the distinct reporter
// count below quorum the target stays Suspect/Alive. Clearing has NO effect on
// a target already promoted to FAIL (terminal). Both nodes must be members.
func (d *Detector) ClearPFAIL(reporter, target NodeID) error {
	if !d.isMember(reporter) || !d.isMember(target) {
		return ErrUnknownNode
	}
	if _, dead := d.dead[target]; dead {
		return nil
	}
	if s := d.susp[target]; s != nil {
		delete(s.reporters, reporter)
		if len(s.reporters) == 0 {
			delete(d.susp, target)
		}
	}
	return nil
}

// promote marks target Dead and fires OnDead exactly once.
func (d *Detector) promote(target NodeID) {
	if _, already := d.dead[target]; already {
		return
	}
	d.dead[target] = struct{}{}
	delete(d.susp, target)
	if d.onDead != nil {
		d.onDead(target)
	}
}

// State returns the current confirmation state of target. Unknown (non-member)
// targets are reported as Alive. Expired suspicions (under an active TTL) are
// pruned as part of evaluation.
func (d *Detector) State(target NodeID) State {
	if _, dead := d.dead[target]; dead {
		return Dead
	}
	if d.liveReporterCount(target) > 0 {
		return Suspect
	}
	return Alive
}

// ReporterCount returns the number of distinct, non-expired reporters currently
// holding a PFAIL for target. Useful as before/after evidence in tests.
func (d *Detector) ReporterCount(target NodeID) int {
	if _, dead := d.dead[target]; dead {
		return d.quorum // confirmed: at least quorum distinct reporters
	}
	return d.liveReporterCount(target)
}
