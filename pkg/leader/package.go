// Package leader provides distributed leader election utilities for Helix Cluster OS.
package leader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/swim"
)

var (
	ErrNoHealthyMembers = errors.New("no healthy members available for election")
	ErrNotLeader        = errors.New("this node is not the leader")
)

// Election implements a distributed leader election using SWIM membership.
// The leader is deterministically chosen as the lowest SHA256 hash of (memberID + term),
// and must renew its leadership within a TTL or another node will take over.
type Election struct {
	mu sync.RWMutex

	localID     string
	term        uint64
	isLeader    bool
	leaderID    string
	lastElected time.Time
	ttl         time.Duration

	// clock is an injectable time source. Defaults to a real-time clock.
	// It exists so failover/expiry can be tested deterministically with a fake
	// clock, with NO wall-clock dependence.
	clock Clock

	// Fencing / lease state (additive hardening; see fencing.go).
	// epoch is a monotonically increasing fencing token issued on each lease
	// acquisition. leaseExpiry is the clock time at which the current lease
	// becomes invalid. registry, when set, provides the cross-node split-brain
	// guard.
	epoch       uint64
	leaseExpiry time.Time
	registry    *LeaseRegistry

	// swimProtocol provides cluster membership awareness
	swimProtocol *swim.Protocol

	// stopCh signals background renewal goroutine to stop
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// Config holds election configuration.
type Config struct {
	LocalID string
	TTL     time.Duration // default 5s
}

// Option configures an Election at construction time. Options are additive and
// optional, preserving the original single-argument NewElection call sites.
type Option func(*Election)

// WithClock injects a custom time source (see Clock). This is the seam that makes
// failover and TTL/lease expiry testable deterministically with a fake clock,
// without any real time.Sleep or wall-clock dependence. If not supplied, the
// Election uses a real-time clock.
func WithClock(c Clock) Option {
	return func(e *Election) {
		if c != nil {
			e.clock = c
		}
	}
}

// NewElection creates a new distributed Election. Additional behavior may be
// supplied via Options (e.g. WithClock); with no options the construction is
// identical to the original API.
func NewElection(cfg *Config, opts ...Option) (*Election, error) {
	if cfg.LocalID == "" {
		return nil, errors.New("local_id is required")
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	e := &Election{
		localID: cfg.LocalID,
		ttl:     ttl,
		clock:   realClock{},
		stopCh:  make(chan struct{}),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e, nil
}

// SetSWIMProtocol attaches a SWIM protocol for cluster membership awareness.
func (e *Election) SetSWIMProtocol(p *swim.Protocol) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.swimProtocol = p
}

// IsLeader returns true if this instance is currently the leader.
func (e *Election) IsLeader() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.isLeader && e.clock.Now().Sub(e.lastElected) < e.ttl*2
}

// LeaderID returns the current leader's ID (may be empty if unknown).
func (e *Election) LeaderID() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.leaderID
}

// BecomeLeader attempts to become leader by checking deterministic ordering.
// If this node is the lowest hash among healthy members, it becomes leader.
func (e *Election) BecomeLeader() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	candidate, err := e.computeLeaderLocked()
	if err != nil {
		return err
	}
	if candidate != e.localID {
		e.isLeader = false
		e.leaderID = candidate
		return ErrNotLeader
	}

	e.isLeader = true
	e.leaderID = e.localID
	e.lastElected = e.clock.Now()
	e.term++
	return nil
}

// Resign resigns leadership.
func (e *Election) Resign() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.isLeader = false
	if e.leaderID == e.localID {
		e.leaderID = ""
	}
}

// Run starts a background goroutine that periodically attempts to become leader
// and resigns if the TTL expires without renewal.
func (e *Election) Run(ctx context.Context) {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		ticker := time.NewTicker(e.ttl / 2)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-e.stopCh:
				return
			case <-ticker.C:
				if e.IsLeader() {
					// Renew leadership
					e.mu.Lock()
					e.lastElected = e.clock.Now()
					e.mu.Unlock()
				} else {
					// Try to acquire leadership
					_ = e.BecomeLeader()
				}
			}
		}
	}()
}

// Stop stops the background goroutine.
func (e *Election) Stop() {
	close(e.stopCh)
	e.wg.Wait()
}

// computeLeaderLocked deterministically picks the leader from healthy members.
// Must be called with e.mu held.
func (e *Election) computeLeaderLocked() (string, error) {
	members := []string{e.localID}
	if e.swimProtocol != nil {
		for _, m := range e.swimProtocol.Members() {
			if m.IsHealthy() {
				members = append(members, m.ID)
			}
		}
	}
	if len(members) == 0 {
		return "", ErrNoHealthyMembers
	}

	termStr := fmt.Sprintf("%d", e.term)
	sort.SliceStable(members, func(i, j int) bool {
		hi := sha256.Sum256([]byte(members[i] + termStr))
		hj := sha256.Sum256([]byte(members[j] + termStr))
		return hex.EncodeToString(hi[:]) < hex.EncodeToString(hj[:])
	})
	return members[0], nil
}
