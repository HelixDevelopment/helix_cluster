// Package discovery provides service discovery for Helix Cluster OS.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrInstanceNotFound = errors.New("instance not found")
	ErrNilInstance      = errors.New("instance is nil")
	ErrMissingFields    = errors.New("instance id and service are required")
)

// Instance represents a service instance.
type Instance struct {
	ID       string
	Service  string
	Address  string
	Port     int
	Metadata map[string]string
	Healthy  bool
	LastSeen time.Time
	TTL      time.Duration
	// Weight biases health-aware load balancing. A value <= 0 is treated as 1.
	Weight int
}

// EventType describes a registry change event.
type EventType int

const (
	EventRegister EventType = iota
	EventDeregister
	EventHealthChange
)

// Event is a registry change notification.
type Event struct {
	Type     EventType
	Service  string
	Instance *Instance
}

// Registry is the service registry interface.
type Registry interface {
	Register(ctx context.Context, inst *Instance) error
	Deregister(ctx context.Context, service, id string) error
	Lookup(ctx context.Context, service string) ([]*Instance, error)
	Watch(ctx context.Context, service string) (<-chan Event, error)
}

// Backend is the storage backend for the registry.
type Backend interface {
	Put(ctx context.Context, key string, inst *Instance) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]*Instance, error)
	Watch(ctx context.Context, prefix string) (<-chan BackendEvent, error)
}

// BackendEvent is a low-level backend change.
type BackendEvent struct {
	Type     EventType
	Key      string
	Instance *Instance
}

// InMemoryBackend is a non-persistent backend for testing and static setups.
type InMemoryBackend struct {
	mu       sync.RWMutex
	data     map[string]*Instance
	watchers map[string][]chan BackendEvent
}

// NewInMemoryBackend creates a new in-memory backend.
func NewInMemoryBackend() *InMemoryBackend {
	return &InMemoryBackend{
		data:     make(map[string]*Instance),
		watchers: make(map[string][]chan BackendEvent),
	}
}

func instanceKey(service, id string) string {
	return fmt.Sprintf("%s/%s", service, id)
}

// Put stores an instance.
func (b *InMemoryBackend) Put(ctx context.Context, key string, inst *Instance) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	copyInst := *inst
	b.data[key] = &copyInst
	b.notify(key, BackendEvent{Type: EventRegister, Key: key, Instance: &copyInst})
	return nil
}

// Delete removes an instance.
func (b *InMemoryBackend) Delete(ctx context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	inst, ok := b.data[key]
	if !ok {
		return fmt.Errorf("instance not found: %s: %w", key, ErrInstanceNotFound)
	}
	delete(b.data, key)
	b.notify(key, BackendEvent{Type: EventDeregister, Key: key, Instance: inst})
	return nil
}

// List returns all instances matching the prefix.
func (b *InMemoryBackend) List(ctx context.Context, prefix string) ([]*Instance, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out []*Instance
	for k, v := range b.data {
		if len(prefix) == 0 || len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			copyInst := *v
			out = append(out, &copyInst)
		}
	}
	return out, nil
}

// Watch returns a channel of backend events for keys matching the prefix.
func (b *InMemoryBackend) Watch(ctx context.Context, prefix string) (<-chan BackendEvent, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan BackendEvent, 10)
	b.watchers[prefix] = append(b.watchers[prefix], ch)
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		defer b.mu.Unlock()
		for i, c := range b.watchers[prefix] {
			if c == ch {
				b.watchers[prefix] = append(b.watchers[prefix][:i], b.watchers[prefix][i+1:]...)
				break
			}
		}
		close(ch)
	}()
	return ch, nil
}

func (b *InMemoryBackend) notify(key string, evt BackendEvent) {
	for prefix, chs := range b.watchers {
		if len(prefix) == 0 || (len(key) >= len(prefix) && key[:len(prefix)] == prefix) {
			for _, ch := range chs {
				select {
				case ch <- evt:
				default:
				}
			}
		}
	}
}

// Clock abstracts time so TTL expiry can be tested deterministically without
// real sleeps. The zero value (nil) defaults to systemClock (time.Now).
type Clock interface {
	Now() time.Time
}

// systemClock is the default real-time Clock.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Option configures a ServiceRegistry. Options are applied additively and do
// not alter the existing default behavior unless explicitly set.
type Option func(*ServiceRegistry)

// WithClock injects a custom Clock for deterministic TTL expiry/sweep. Passing
// a nil clock is a no-op (the default systemClock is retained).
func WithClock(clk Clock) Option {
	return func(r *ServiceRegistry) {
		if clk != nil {
			r.clock = clk
		}
	}
}

// ServiceRegistry is a high-level registry with TTL health and watch support.
type ServiceRegistry struct {
	mu         sync.RWMutex
	backend    Backend
	instances  map[string]*Instance // local cache
	ttlChecker *time.Ticker
	stopCh     chan struct{}
	clock      Clock
	selector   *WeightedSelector
}

// NewServiceRegistry creates a registry backed by the given backend.
func NewServiceRegistry(backend Backend, opts ...Option) *ServiceRegistry {
	r := &ServiceRegistry{
		backend:   backend,
		instances: make(map[string]*Instance),
		stopCh:    make(chan struct{}),
		clock:     systemClock{},
		selector:  NewWeightedSelector(),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// NewStaticRegistry creates a registry with an in-memory backend.
func NewStaticRegistry(opts ...Option) *ServiceRegistry {
	return NewServiceRegistry(NewInMemoryBackend(), opts...)
}

// now returns the registry's current time via its injected Clock.
func (r *ServiceRegistry) now() time.Time {
	if r.clock == nil {
		return time.Now()
	}
	return r.clock.Now()
}

// Start begins the TTL health checker.
func (r *ServiceRegistry) Start(interval time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ttlChecker != nil {
		return
	}
	r.ttlChecker = time.NewTicker(interval)
	go r.runTTLChecker()
}

// Stop halts the TTL health checker.
func (r *ServiceRegistry) Stop() {
	r.mu.Lock()
	if r.ttlChecker != nil {
		r.ttlChecker.Stop()
		r.ttlChecker = nil
	}
	select {
	case <-r.stopCh:
		// already closed
	default:
		close(r.stopCh)
	}
	r.mu.Unlock()
}

func (r *ServiceRegistry) runTTLChecker() {
	for {
		r.mu.Lock()
		ticker := r.ttlChecker
		r.mu.Unlock()
		if ticker == nil {
			return
		}
		select {
		case <-ticker.C:
			r.checkTTLs()
		case <-r.stopCh:
			return
		}
	}
}

func (r *ServiceRegistry) checkTTLs() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepExpiredLocked()
}

// SweepExpired performs a single deterministic TTL sweep using the registry's
// injected Clock, evicting any expired instances from the local cache and the
// backend. It is safe to call directly (e.g. in tests) without starting the
// background ticker, enabling expiry validation without real sleeps.
func (r *ServiceRegistry) SweepExpired() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepExpiredLocked()
}

// sweepExpiredLocked evicts expired instances. Caller must hold r.mu.
func (r *ServiceRegistry) sweepExpiredLocked() {
	now := r.now()
	for key, inst := range r.instances {
		if inst.TTL > 0 && now.Sub(inst.LastSeen) > inst.TTL {
			inst.Healthy = false
			// Notify watchers via backend delete so they get deregister event
			deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = r.backend.Delete(deleteCtx, key)
			deleteCancel()
			delete(r.instances, key)
		}
	}
}

// Register registers an instance with optional TTL.
func (r *ServiceRegistry) Register(ctx context.Context, inst *Instance) error {
	if inst == nil {
		return fmt.Errorf("instance is nil: %w", ErrNilInstance)
	}
	if inst.ID == "" || inst.Service == "" {
		return fmt.Errorf("instance id and service are required: %w", ErrMissingFields)
	}
	if inst.LastSeen.IsZero() {
		inst.LastSeen = r.now()
	}
	if inst.TTL == 0 {
		inst.TTL = 30 * time.Second
	}
	key := instanceKey(inst.Service, inst.ID)

	r.mu.Lock()
	cacheInst := &Instance{
		ID:       inst.ID,
		Service:  inst.Service,
		Address:  inst.Address,
		Port:     inst.Port,
		Metadata: inst.Metadata,
		Healthy:  true,
		LastSeen: inst.LastSeen,
		TTL:      inst.TTL,
		Weight:   inst.Weight,
	}
	r.instances[key] = cacheInst
	backendInst := *cacheInst
	r.mu.Unlock()

	return r.backend.Put(ctx, key, &backendInst)
}

// Deregister removes an instance.
func (r *ServiceRegistry) Deregister(ctx context.Context, service, id string) error {
	key := instanceKey(service, id)
	r.mu.Lock()
	delete(r.instances, key)
	r.mu.Unlock()
	return r.backend.Delete(ctx, key)
}

// Lookup returns healthy instances for a service.
func (r *ServiceRegistry) Lookup(ctx context.Context, service string) ([]*Instance, error) {
	prefix := service + "/"
	all, err := r.backend.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	var out []*Instance
	for _, inst := range all {
		copyInst := *inst
		if copyInst.Healthy {
			out = append(out, &copyInst)
		}
	}
	return out, nil
}

// Watch returns a channel of registry events for a service.
func (r *ServiceRegistry) Watch(ctx context.Context, service string) (<-chan Event, error) {
	prefix := service + "/"
	backendCh, err := r.backend.Watch(ctx, prefix)
	if err != nil {
		return nil, err
	}
	outCh := make(chan Event, 10)
	go func() {
		defer close(outCh)
		for be := range backendCh {
			outCh <- Event{
				Type:     be.Type,
				Service:  service,
				Instance: be.Instance,
			}
		}
	}()
	return outCh, nil
}

// Renew updates the LastSeen timestamp for an instance (heartbeat).
func (r *ServiceRegistry) Renew(ctx context.Context, service, id string) error {
	key := instanceKey(service, id)
	r.mu.Lock()
	inst, ok := r.instances[key]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("instance not found: %s: %w", key, ErrInstanceNotFound)
	}
	inst.LastSeen = r.now()
	inst.Healthy = true
	copyInst := *inst
	r.mu.Unlock()
	return r.backend.Put(ctx, key, &copyInst)
}

// HealthyInstances returns only healthy instances for a service.
func (r *ServiceRegistry) HealthyInstances(ctx context.Context, service string) ([]*Instance, error) {
	return r.Lookup(ctx, service)
}
