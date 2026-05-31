package swim

import (
	"sync"
	"time"
)

// Prober abstracts the act of probing a member, both directly and indirectly
// (via k relay members). It is injectable so the suspicion machinery can be
// tested deterministically without a real network.
//
//   - DirectProbe pings target directly and reports whether an ack was received.
//   - IndirectProbe asks the supplied relay members to ping target on this
//     node's behalf and reports whether any relay observed the target alive.
//
// Implementations MUST be safe for concurrent use.
type Prober interface {
	DirectProbe(target *Member) bool
	IndirectProbe(target *Member, relays []*Member) bool
}

// SuspicionConfig configures a SuspicionManager.
type SuspicionConfig struct {
	// Clock is the injectable time source. Defaults to SystemClock.
	Clock Clock
	// Prober performs direct/indirect probes. Required for indirect probing on
	// timeout; if nil, indirect probing is skipped and the node dies on timeout.
	Prober Prober
	// SuspicionTimeout is how long a member may remain Suspect (without a
	// refutation) before it is declared Dead. Defaults to 5s.
	SuspicionTimeout time.Duration
	// IndirectProbes is the number (k) of relay members asked to probe the
	// target when the suspicion timeout fires. Defaults to 3.
	IndirectProbes int
	// Relays is an optional static set of relay members used for indirect
	// probing. In production this is supplied dynamically via SuspectWithRelays.
	Relays []*Member
	// OnDead is invoked exactly once when a member is confirmed Dead.
	OnDead func(memberID string)
	// OnRefute is invoked when an active suspicion is cleared by refutation.
	OnRefute func(memberID string)
}

// SuspicionManager implements SWIM suspicion with incarnation-number refutation
// and a suspicion->Dead timeout that first attempts indirect probes through k
// relay members. It is deterministic when supplied a ManualClock and a test
// Prober, and is fully backward-compatible: it is an additive type that does
// not alter the existing FailureDetector API.
type SuspicionManager struct {
	mu       sync.Mutex
	clock    Clock
	prober   Prober
	timeout  time.Duration
	k        int
	relays   []*Member
	onDead   func(string)
	onRefute func(string)
	records  map[string]*suspicionEntry
}

type suspicionEntry struct {
	member      *Member
	incarnation uint32
	relays      []*Member
	stop        func() bool
	dead        bool
}

// NewSuspicionManager creates a SuspicionManager from cfg, applying defaults.
func NewSuspicionManager(cfg SuspicionConfig) *SuspicionManager {
	clock := cfg.Clock
	if clock == nil {
		clock = NewSystemClock()
	}
	timeout := cfg.SuspicionTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	k := cfg.IndirectProbes
	if k <= 0 {
		k = 3
	}
	return &SuspicionManager{
		clock:    clock,
		prober:   cfg.Prober,
		timeout:  timeout,
		k:        k,
		relays:   cfg.Relays,
		onDead:   cfg.OnDead,
		onRefute: cfg.OnRefute,
		records:  make(map[string]*suspicionEntry),
	}
}

// Suspect transitions member to Suspect at the given incarnation and arms the
// suspicion timeout. It returns false (and changes nothing) if the member's
// state machine rejects the transition (e.g. a stale incarnation). Relays
// configured on the manager are used for the eventual indirect probe.
func (sm *SuspicionManager) Suspect(member *Member, incarnation uint32) bool {
	return sm.SuspectWithRelays(member, incarnation, sm.relays)
}

// SuspectWithRelays is like Suspect but uses the supplied relay members for the
// indirect probe when the suspicion timeout fires. This is the production entry
// point where the caller selects k random alive members at suspicion time.
func (sm *SuspicionManager) SuspectWithRelays(member *Member, incarnation uint32, relays []*Member) bool {
	if member == nil {
		return false
	}
	// Honour the existing Member state-machine semantics for incarnation.
	if !member.UpdateState(StateSuspect, incarnation) {
		return false
	}

	sm.mu.Lock()
	if existing, ok := sm.records[member.ID]; ok {
		if existing.stop != nil {
			existing.stop()
		}
		delete(sm.records, member.ID)
	}
	entry := &suspicionEntry{
		member:      member,
		incarnation: incarnation,
		relays:      relays,
	}
	sm.records[member.ID] = entry
	id := member.ID
	entry.stop = sm.clock.AfterFunc(sm.timeout, func() {
		sm.onTimeout(id)
	})
	sm.mu.Unlock()
	return true
}

// Refute attempts to clear an active suspicion of memberID using a refuting
// incarnation. A refutation succeeds only when refuteIncarnation is strictly
// higher than the incarnation that the suspicion was raised at (mirroring SWIM:
// a higher incarnation overrides Suspect with Alive). On success the member is
// returned to Alive at the refuting incarnation, the timeout is cancelled, and
// the member can no longer be declared Dead for that suspicion. Returns whether
// a suspicion was actually refuted.
func (sm *SuspicionManager) Refute(memberID string, refuteIncarnation uint32) bool {
	sm.mu.Lock()
	entry, ok := sm.records[memberID]
	if !ok || entry.dead {
		sm.mu.Unlock()
		return false
	}
	if refuteIncarnation <= entry.incarnation {
		// Not a strictly higher incarnation: cannot override Suspect.
		sm.mu.Unlock()
		return false
	}
	if entry.stop != nil {
		entry.stop()
	}
	delete(sm.records, memberID)
	member := entry.member
	sm.mu.Unlock()

	// Higher incarnation overrides Suspect with Alive.
	member.UpdateState(StateAlive, refuteIncarnation)
	if sm.onRefute != nil {
		sm.onRefute(memberID)
	}
	return true
}

// IsSuspected reports whether memberID currently has an active suspicion.
func (sm *SuspicionManager) IsSuspected(memberID string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	e, ok := sm.records[memberID]
	return ok && !e.dead
}

// SuspectCount returns the number of members currently under suspicion.
func (sm *SuspicionManager) SuspectCount() int {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	n := 0
	for _, e := range sm.records {
		if !e.dead {
			n++
		}
	}
	return n
}

// onTimeout is invoked by the clock when a suspicion's timeout elapses. Before
// declaring the member Dead it performs a last-chance indirect probe through up
// to k relays; if any relay observes the target alive, the suspicion is cleared
// (the member is refuted) instead of killed.
func (sm *SuspicionManager) onTimeout(memberID string) {
	sm.mu.Lock()
	entry, ok := sm.records[memberID]
	if !ok || entry.dead {
		sm.mu.Unlock()
		return
	}
	member := entry.member
	relays := sm.selectRelaysLocked(entry)
	prober := sm.prober
	sm.mu.Unlock()

	// Last-chance indirect probe before declaring Dead.
	if prober != nil && len(relays) > 0 {
		if prober.IndirectProbe(member, relays) {
			// Target is reachable via a relay: refute the suspicion.
			sm.mu.Lock()
			cur, ok := sm.records[memberID]
			if ok && !cur.dead {
				delete(sm.records, memberID)
			}
			sm.mu.Unlock()
			// Bump incarnation so the recovery overrides the Suspect locally.
			member.UpdateState(StateAlive, member.Incarnation+1)
			if sm.onRefute != nil {
				sm.onRefute(memberID)
			}
			return
		}
	}

	// No refutation and no successful indirect probe: confirm Dead.
	sm.mu.Lock()
	cur, ok := sm.records[memberID]
	if !ok || cur.dead {
		sm.mu.Unlock()
		return
	}
	cur.dead = true
	delete(sm.records, memberID)
	inc := cur.incarnation
	sm.mu.Unlock()

	member.UpdateState(StateDead, inc)
	if sm.onDead != nil {
		sm.onDead(memberID)
	}
}

// selectRelaysLocked returns at most k relay members for the indirect probe,
// excluding the suspected target itself. Caller must hold sm.mu.
func (sm *SuspicionManager) selectRelaysLocked(entry *suspicionEntry) []*Member {
	src := entry.relays
	if len(src) == 0 {
		src = sm.relays
	}
	out := make([]*Member, 0, sm.k)
	for _, r := range src {
		if r == nil || r.ID == entry.member.ID {
			continue
		}
		out = append(out, r)
		if len(out) >= sm.k {
			break
		}
	}
	return out
}
