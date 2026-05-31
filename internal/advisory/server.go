// Package advisory implements the Advisory Lock gRPC service.
package advisory

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	eventAcquired = "acquired"
	eventReleased = "released"
	defaultTTL    = 30 * time.Second
)

// lockEntry represents a held lock with expiration.
type lockEntry struct {
	resource string
	holder   string
	lockID   string
	expires  time.Time
}

// lockManager is an in-memory advisory lock manager with TTL support.
type lockManager struct {
	mu      sync.RWMutex
	locks   map[string]*lockEntry        // resource -> entry
	waiters map[string][]chan struct{}   // resource -> channels waiting for release
	subs    map[string][]chan *helixv1.LockEvent // resource prefix -> subscribers
}

func newLockManager() *lockManager {
	lm := &lockManager{
		locks:   make(map[string]*lockEntry),
		waiters: make(map[string][]chan struct{}),
		subs:    make(map[string][]chan *helixv1.LockEvent),
	}
	go lm.expirationLoop()
	return lm
}

func (lm *lockManager) expirationLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		lm.mu.Lock()
		now := time.Now()
		for res, entry := range lm.locks {
			if now.After(entry.expires) {
				delete(lm.locks, res)
				lm.notify(res, "", eventReleased)
				lm.wakeWaiters(res)
			}
		}
		lm.mu.Unlock()
	}
}

func (lm *lockManager) wakeWaiters(resource string) {
	for _, ch := range lm.waiters[resource] {
		close(ch)
	}
	delete(lm.waiters, resource)
}

func (lm *lockManager) notify(resource, holder, eventType string) {
	event := &helixv1.LockEvent{
		Resource:  resource,
		Holder:    holder,
		EventType: eventType,
		Timestamp: time.Now().Unix(),
	}
	for prefix, subs := range lm.subs {
		if strings.HasPrefix(resource, prefix) {
			for _, ch := range subs {
				select {
				case ch <- event:
				default:
				}
			}
		}
	}
}

func (lm *lockManager) tryLock(resource, holder string, ttl time.Duration) (string, bool) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	if entry, ok := lm.locks[resource]; ok {
		if time.Now().Before(entry.expires) {
			return "", false
		}
		// expired, treat as free
		delete(lm.locks, resource)
		lm.notify(resource, "", eventReleased)
	}

	lockID := uuid.NewString()
	if ttl <= 0 {
		ttl = defaultTTL
	}
	lm.locks[resource] = &lockEntry{
		resource: resource,
		holder:   holder,
		lockID:   lockID,
		expires:  time.Now().Add(ttl),
	}
	lm.notify(resource, holder, eventAcquired)
	return lockID, true
}

func (lm *lockManager) lock(ctx context.Context, resource, holder string, ttl time.Duration) (string, bool) {
	lockID, ok := lm.tryLock(resource, holder, ttl)
	if ok {
		return lockID, true
	}

	for {
		lm.mu.Lock()
		entry, exists := lm.locks[resource]
		if !exists || time.Now().After(entry.expires) {
			lm.mu.Unlock()
			return lm.tryLock(resource, holder, ttl)
		}
		ch := make(chan struct{})
		lm.waiters[resource] = append(lm.waiters[resource], ch)
		lm.mu.Unlock()

		select {
		case <-ch:
			// lock was released or expired, try again
			continue
		case <-ctx.Done():
			// remove ourselves from waiters
			lm.mu.Lock()
			waiters := lm.waiters[resource]
			for i, w := range waiters {
				if w == ch {
					lm.waiters[resource] = append(waiters[:i], waiters[i+1:]...)
					break
				}
			}
			lm.mu.Unlock()
			return "", false
		}
	}
}

func (lm *lockManager) unlock(resource, holder string) (bool, error) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	entry, ok := lm.locks[resource]
	if !ok {
		return false, fmt.Errorf("lock not found for resource %q", resource)
	}
	if entry.holder != holder {
		return false, fmt.Errorf("holder %q does not own lock on %q", holder, resource)
	}
	delete(lm.locks, resource)
	lm.notify(resource, "", eventReleased)
	lm.wakeWaiters(resource)
	return true, nil
}

func (lm *lockManager) subscribe(prefix string) chan *helixv1.LockEvent {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	ch := make(chan *helixv1.LockEvent, 16)
	lm.subs[prefix] = append(lm.subs[prefix], ch)
	return ch
}

func (lm *lockManager) unsubscribe(prefix string, ch chan *helixv1.LockEvent) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	subs := lm.subs[prefix]
	for i, s := range subs {
		if s == ch {
			lm.subs[prefix] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	close(ch)
}

// Server implements helixv1.AdvisoryServiceServer.
type Server struct {
	helixv1.UnimplementedAdvisoryServiceServer
	lm *lockManager
}

// NewServer creates a new advisory lock server.
func NewServer() *Server {
	return &Server{lm: newLockManager()}
}

// Lock acquires a lock on a resource, blocking until acquired.
func (s *Server) Lock(ctx context.Context, req *helixv1.LockRequest) (*helixv1.LockResponse, error) {
	if req.Resource == "" {
		return nil, status.Error(codes.InvalidArgument, "resource is required")
	}
	if req.Holder == "" {
		return nil, status.Error(codes.InvalidArgument, "holder is required")
	}
	ttl := time.Duration(req.TtlSeconds) * time.Second
	lockID, acquired := s.lm.lock(ctx, req.Resource, req.Holder, ttl)
	if !acquired {
		return &helixv1.LockResponse{Acquired: false}, nil
	}
	return &helixv1.LockResponse{Acquired: true, LockId: lockID}, nil
}

// Unlock releases a lock by its holder.
func (s *Server) Unlock(ctx context.Context, req *helixv1.UnlockRequest) (*helixv1.UnlockResponse, error) {
	if req.Resource == "" {
		return nil, status.Error(codes.InvalidArgument, "resource is required")
	}
	if req.Holder == "" {
		return nil, status.Error(codes.InvalidArgument, "holder is required")
	}
	released, err := s.lm.unlock(req.Resource, req.Holder)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &helixv1.UnlockResponse{Released: released}, nil
}

// TryLock attempts to acquire a lock and returns immediately.
func (s *Server) TryLock(ctx context.Context, req *helixv1.TryLockRequest) (*helixv1.TryLockResponse, error) {
	if req.Resource == "" {
		return nil, status.Error(codes.InvalidArgument, "resource is required")
	}
	if req.Holder == "" {
		return nil, status.Error(codes.InvalidArgument, "holder is required")
	}
	ttl := time.Duration(req.TtlSeconds) * time.Second
	lockID, acquired := s.lm.tryLock(req.Resource, req.Holder, ttl)
	return &helixv1.TryLockResponse{Acquired: acquired, LockId: lockID}, nil
}

// WatchLocks streams lock events matching the resource prefix.
func (s *Server) WatchLocks(req *helixv1.WatchLocksRequest, stream helixv1.AdvisoryService_WatchLocksServer) error {
	prefix := req.ResourcePrefix
	ch := s.lm.subscribe(prefix)
	defer s.lm.unsubscribe(prefix, ch)

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(event); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}
