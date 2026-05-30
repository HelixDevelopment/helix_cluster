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

	localID    string
	term       uint64
	isLeader   bool
	leaderID   string
	lastElected time.Time
	ttl        time.Duration

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

// NewElection creates a new distributed Election.
func NewElection(cfg *Config) (*Election, error) {
	if cfg.LocalID == "" {
		return nil, errors.New("local_id is required")
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	return &Election{
		localID: cfg.LocalID,
		ttl:     ttl,
		stopCh:  make(chan struct{}),
	}, nil
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
	return e.isLeader && time.Since(e.lastElected) < e.ttl*2
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
	e.lastElected = time.Now()
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
					e.lastElected = time.Now()
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
