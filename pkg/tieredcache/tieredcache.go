// Package tieredcache implements a three-tier cache: Hot (in-memory),
// Warm (NVMe-class backend), and Cold (SSD-class backend).
//
// The Warm and Cold tiers are injected via the Tier interface (CLAUDE-2 seam:
// real NVMe/SSD hardware is not available in this environment; callers supply
// real or test implementations). The cache uses an injected now func so that
// tests never touch the wall clock.
//
// Demotion policy: a maintenance pass (RunMaintenance) inspects every Hot key.
// If the key has not been accessed within the idle threshold, it is demoted
// Hot -> Warm. If it was already Warm and idle, it is demoted Warm -> Cold.
// Promotion policy: a Get that finds the key in Warm or Cold promotes it back
// to Hot, counting a hit on the tier where it was found.
//
// Mutation guard (TestIdleKeyDemotesThroughTiers):
//
//	guard: key.lastAccess.Before(now().Add(-c.idleThreshold))
//	  in RunMaintenance — if this comparison is inverted (or replaced with <=)
//	  an idle key will never demote and TestIdleKeyDemotesThroughTiers will FAIL.
package tieredcache

import (
	"errors"
	"sync"
	"time"
)

// ErrCapabilityAbsent is returned when a real hardware capability (NVMe, SSD)
// is genuinely absent — the caller's Tier.Get/Put implementation should return
// this to signal that the tier is unavailable rather than silently failing.
var ErrCapabilityAbsent = errors.New("tieredcache: hardware capability absent")

// ErrNotFound is returned by Tier.Get when the key is not present in that tier.
var ErrNotFound = errors.New("tieredcache: key not found")

// Tier is the interface for a backing storage tier (Warm / Cold).
// Implementors must return ErrNotFound when a key is absent and
// ErrCapabilityAbsent when the underlying hardware is unavailable.
// AccessLatency returns the measured or declared latency for this tier;
// it must be strictly greater than the latency of any hotter tier.
// Delete removes a key from the tier; it is a no-op when the key is absent.
type Tier interface {
	Get(key string) ([]byte, error)
	Put(key string, value []byte) error
	Delete(key string) error
	AccessLatency() time.Duration
}

// TierName identifies which cache tier a record was served from.
type TierName string

const (
	TierHot  TierName = "hot"
	TierWarm TierName = "warm"
	TierCold TierName = "cold"
)

// Record is the value stored in the Hot (in-memory) tier together with its
// access metadata.
type Record struct {
	Value      []byte
	lastAccess time.Time // updated on every Get; measured by the injected now func
}

// Stats carries per-tier hit counters. A hit is counted on the tier where the
// key was actually found (Hot, Warm, or Cold).
type Stats struct {
	HotHits  int64
	WarmHits int64
	ColdHits int64
}

// Cache is a three-tier cache. All public methods are safe for concurrent use.
type Cache struct {
	mu sync.Mutex

	hot          map[string]*Record
	warm         Tier
	cold         Tier
	idleThreshold time.Duration
	now          func() time.Time

	stats Stats
}

// Config holds the parameters required to construct a Cache.
type Config struct {
	// Warm is the NVMe-class backing tier (must not be nil).
	Warm Tier
	// Cold is the SSD-class backing tier (must not be nil).
	Cold Tier
	// IdleThreshold is the duration after which an un-accessed Hot key is
	// eligible for demotion on the next maintenance pass.
	IdleThreshold time.Duration
	// Now is an injected clock function; pass time.Now for production.
	// Tests MUST inject a controllable clock.
	Now func() time.Time
}

// New validates Config and returns a ready Cache.
// It returns an error if latencies do not satisfy Hot < Warm < Cold, or if any
// required field is nil/zero.
func New(cfg Config) (*Cache, error) {
	if cfg.Warm == nil {
		return nil, errors.New("tieredcache: Warm tier must not be nil")
	}
	if cfg.Cold == nil {
		return nil, errors.New("tieredcache: Cold tier must not be nil")
	}
	if cfg.IdleThreshold <= 0 {
		return nil, errors.New("tieredcache: IdleThreshold must be positive")
	}
	if cfg.Now == nil {
		return nil, errors.New("tieredcache: Now func must not be nil")
	}

	// Enforce strictly increasing latency: Hot < Warm < Cold.
	// Hot in-memory access latency is defined as 0 (no round-trip).
	hotLatency := time.Duration(0)
	warmLatency := cfg.Warm.AccessLatency()
	coldLatency := cfg.Cold.AccessLatency()
	if warmLatency <= hotLatency {
		return nil, errors.New("tieredcache: Warm.AccessLatency must be > 0 (Hot latency)")
	}
	if coldLatency <= warmLatency {
		return nil, errors.New("tieredcache: Cold.AccessLatency must be > Warm.AccessLatency")
	}

	return &Cache{
		hot:           make(map[string]*Record),
		warm:          cfg.Warm,
		cold:          cfg.Cold,
		idleThreshold: cfg.IdleThreshold,
		now:           cfg.Now,
	}, nil
}

// Put stores value under key in the Hot tier only.
// Warm and Cold tiers are populated exclusively by demotion (RunMaintenance),
// which ensures the tier-residency state machine is consistent.
//
// Put copies value so that the caller's buffer can be reused or mutated after
// the call without corrupting the cached record.
func (c *Cache) Put(key string, value []byte) error {
	cp := make([]byte, len(value))
	copy(cp, value)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.hot[key] = &Record{
		Value:      cp,
		lastAccess: c.now(),
	}
	return nil
}

// Get looks up key, first in Hot, then Warm, then Cold.
// On a hit in Warm or Cold the key is promoted back to Hot.
// Returns the value, the tier it was found in, and any error.
func (c *Cache) Get(key string) ([]byte, TierName, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Hot tier lookup.
	if rec, ok := c.hot[key]; ok {
		rec.lastAccess = c.now()
		c.stats.HotHits++
		// Return a copy so the caller cannot alias the internal record.
		out := make([]byte, len(rec.Value))
		copy(out, rec.Value)
		return out, TierHot, nil
	}

	// Warm tier lookup.
	val, err := c.warm.Get(key)
	if err == nil {
		// Promote to Hot. Store an independent copy in Hot; return another copy
		// to the caller so neither can alias the other's buffer.
		stored := make([]byte, len(val))
		copy(stored, val)
		c.hot[key] = &Record{Value: stored, lastAccess: c.now()}
		c.stats.WarmHits++
		out := make([]byte, len(val))
		copy(out, val)
		return out, TierWarm, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, TierWarm, err
	}

	// Cold tier lookup.
	val, err = c.cold.Get(key)
	if err == nil {
		// Promote to Hot (and re-populate Warm). Store an independent copy in Hot;
		// return another copy to the caller.
		stored := make([]byte, len(val))
		copy(stored, val)
		c.hot[key] = &Record{Value: stored, lastAccess: c.now()}
		_ = c.warm.Put(key, val)
		c.stats.ColdHits++
		out := make([]byte, len(val))
		copy(out, val)
		return out, TierCold, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, TierCold, err
	}

	return nil, "", ErrNotFound
}

// RunMaintenance inspects every Hot key and demotes idle ones.
//
// Mutation guard — pinned by TestIdleKeyDemotesThroughTiers:
// The guard below (key.lastAccess.Before(now().Add(-c.idleThreshold))) is the
// single-line predicate that decides whether a key is idle. Inverting it (e.g.
// changing Before to After, or negating the threshold) makes an idle key never
// demote and causes TestIdleKeyDemotesThroughTiers to FAIL.
func (c *Cache) RunMaintenance() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	deadline := now.Add(-c.idleThreshold)

	for key, rec := range c.hot {
		// MUTATION GUARD: rec.lastAccess.Before(deadline) — DO NOT invert.
		// Pinned by TestIdleKeyDemotesThroughTiers: inverting this predicate
		// (Before -> After, or negating idleThreshold) prevents demotion and
		// causes that test to FAIL.
		if rec.lastAccess.Before(deadline) {
			// Key is idle: decide demotion tier based on current tier residency.
			//
			// If the key is already in Warm (previous demotion put it there),
			// this is a second-idle pass: cascade all the way to Cold and
			// remove from Warm so that a subsequent Get finds it only in Cold.
			//
			// If the key is NOT yet in Warm, this is the first demotion:
			// move Hot -> Warm.
			_, warmErr := c.warm.Get(key)
			if warmErr == nil {
				// Key already lives in Warm → demote Warm->Cold.
				// Even if Cold.Put fails (e.g. ErrCapabilityAbsent) we still remove
				// from Warm because the maintenance invariant is best-effort; the key
				// will be absent from all tiers rather than stale in Warm.
				_ = c.cold.Put(key, rec.Value)
				_ = c.warm.Delete(key)
				delete(c.hot, key)
			} else if errors.Is(warmErr, ErrNotFound) {
				// Key not yet in Warm → demote Hot->Warm (first demotion).
				// Only evict from Hot when the write to Warm succeeds.
				if putErr := c.warm.Put(key, rec.Value); putErr == nil {
					delete(c.hot, key)
				}
				// If Warm is unavailable, leave the key in Hot so it is not lost.
			}
			// Any other error from warm.Get (e.g. ErrCapabilityAbsent) means the
			// Warm tier is unreadable; skip demotion and leave the key in Hot.
		}
	}
}

// Stats returns a snapshot of the per-tier hit counters.
func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

// HotCount returns the number of keys currently in the Hot tier.
func (c *Cache) HotCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.hot)
}
