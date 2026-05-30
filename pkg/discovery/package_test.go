package discovery

import (
	"context"
	"testing"
	"time"
)

func TestInMemoryBackend_PutAndList(t *testing.T) {
	b := NewInMemoryBackend()
	inst := &Instance{ID: "i1", Service: "svc", Address: "10.0.0.1", Port: 8080}
	if err := b.Put(context.Background(), "svc/i1", inst); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	list, err := b.List(context.Background(), "svc/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 instance, got %d", len(list))
	}
}

func TestInMemoryBackend_PutAndList_Mutation(t *testing.T) {
	// Mutation: List returns all entries regardless of prefix
	b := NewInMemoryBackend()
	_ = b.Put(context.Background(), "svc/i1", &Instance{ID: "i1", Service: "svc"})
	_ = b.Put(context.Background(), "other/i2", &Instance{ID: "i2", Service: "other"})
	list, _ := b.List(context.Background(), "svc/")
	for _, inst := range list {
		if inst.Service != "svc" {
			t.Errorf("expected only svc instances, got %s", inst.Service)
		}
	}
}

func TestInMemoryBackend_Delete(t *testing.T) {
	b := NewInMemoryBackend()
	_ = b.Put(context.Background(), "svc/i1", &Instance{ID: "i1", Service: "svc"})
	if err := b.Delete(context.Background(), "svc/i1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	list, _ := b.List(context.Background(), "svc/")
	if len(list) != 0 {
		t.Errorf("expected 0 instances after delete, got %d", len(list))
	}
}

func TestInMemoryBackend_Delete_Mutation(t *testing.T) {
	// Mutation: Delete does not remove entry
	b := NewInMemoryBackend()
	_ = b.Put(context.Background(), "svc/i1", &Instance{ID: "i1", Service: "svc"})
	_ = b.Delete(context.Background(), "svc/i1")
	list, _ := b.List(context.Background(), "svc/")
	if len(list) != 0 {
		t.Errorf("expected 0 instances after delete, got %d", len(list))
	}
}

func TestInMemoryBackend_Watch(t *testing.T) {
	b := NewInMemoryBackend()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := b.Watch(ctx, "svc/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	inst := &Instance{ID: "i1", Service: "svc", Address: "10.0.0.1", Port: 8080}
	_ = b.Put(ctx, "svc/i1", inst)

	select {
	case evt := <-ch:
		if evt.Type != EventRegister {
			t.Errorf("expected EventRegister, got %d", evt.Type)
		}
		if evt.Instance.ID != "i1" {
			t.Errorf("expected instance i1, got %s", evt.Instance.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for watch event")
	}
}

func TestInMemoryBackend_Watch_Mutation(t *testing.T) {
	// Mutation: Watch does not receive events for matching prefix
	b := NewInMemoryBackend()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := b.Watch(ctx, "svc/")
	_ = b.Put(ctx, "svc/i1", &Instance{ID: "i1", Service: "svc"})

	select {
	case <-ch:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for watch event")
	}
}

func TestServiceRegistry_RegisterAndLookup(t *testing.T) {
	r := NewStaticRegistry()
	inst := &Instance{ID: "i1", Service: "svc", Address: "10.0.0.1", Port: 8080}
	if err := r.Register(context.Background(), inst); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	list, err := r.Lookup(context.Background(), "svc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 instance, got %d", len(list))
	}
	if list[0].ID != "i1" {
		t.Errorf("expected i1, got %s", list[0].ID)
	}
}

func TestServiceRegistry_RegisterAndLookup_Mutation(t *testing.T) {
	// Mutation: Lookup returns unhealthy instances
	r := NewStaticRegistry()
	inst := &Instance{ID: "i1", Service: "svc", Address: "10.0.0.1", Port: 8080}
	_ = r.Register(context.Background(), inst)
	list, _ := r.Lookup(context.Background(), "svc")
	for _, i := range list {
		if !i.Healthy {
			t.Error("expected only healthy instances")
		}
	}
}

func TestServiceRegistry_Deregister(t *testing.T) {
	r := NewStaticRegistry()
	inst := &Instance{ID: "i1", Service: "svc", Address: "10.0.0.1", Port: 8080}
	_ = r.Register(context.Background(), inst)
	if err := r.Deregister(context.Background(), "svc", "i1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	list, _ := r.Lookup(context.Background(), "svc")
	if len(list) != 0 {
		t.Errorf("expected 0 instances, got %d", len(list))
	}
}

func TestServiceRegistry_Deregister_Mutation(t *testing.T) {
	// Mutation: Deregister does not remove from backend
	r := NewStaticRegistry()
	_ = r.Register(context.Background(), &Instance{ID: "i1", Service: "svc"})
	_ = r.Deregister(context.Background(), "svc", "i1")
	list, _ := r.Lookup(context.Background(), "svc")
	if len(list) != 0 {
		t.Errorf("expected 0 instances after deregister, got %d", len(list))
	}
}

func TestServiceRegistry_RegisterValidation(t *testing.T) {
	r := NewStaticRegistry()
	if err := r.Register(context.Background(), nil); err == nil {
		t.Error("expected error for nil instance")
	}
	if err := r.Register(context.Background(), &Instance{Service: "svc"}); err == nil {
		t.Error("expected error for empty id")
	}
	if err := r.Register(context.Background(), &Instance{ID: "i1"}); err == nil {
		t.Error("expected error for empty service")
	}
}

func TestServiceRegistry_Watch(t *testing.T) {
	r := NewStaticRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := r.Watch(ctx, "svc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	inst := &Instance{ID: "i1", Service: "svc", Address: "10.0.0.1", Port: 8080}
	_ = r.Register(ctx, inst)

	select {
	case evt := <-ch:
		if evt.Type != EventRegister {
			t.Errorf("expected EventRegister, got %d", evt.Type)
		}
		if evt.Service != "svc" {
			t.Errorf("expected service svc, got %s", evt.Service)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for registry watch event")
	}
}

func TestServiceRegistry_Watch_Mutation(t *testing.T) {
	// Mutation: Watch does not forward backend events
	r := NewStaticRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := r.Watch(ctx, "svc")
	_ = r.Register(ctx, &Instance{ID: "i1", Service: "svc"})

	select {
	case <-ch:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for registry watch event")
	}
}

func TestServiceRegistry_Renew(t *testing.T) {
	r := NewStaticRegistry()
	inst := &Instance{ID: "i1", Service: "svc", Address: "10.0.0.1", Port: 8080, TTL: time.Minute}
	_ = r.Register(context.Background(), inst)

	oldSeen := inst.LastSeen
	time.Sleep(50 * time.Millisecond)
	if err := r.Renew(context.Background(), "svc", "i1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify via Lookup that the registry's cached instance was updated
	list, _ := r.Lookup(context.Background(), "svc")
	if len(list) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(list))
	}
	if !list[0].LastSeen.After(oldSeen) {
		t.Error("expected LastSeen to be updated after renew")
	}
	if !list[0].Healthy {
		t.Error("expected instance to be healthy after renew")
	}
}

func TestServiceRegistry_Renew_Mutation(t *testing.T) {
	// Mutation: Renew does not update LastSeen
	r := NewStaticRegistry()
	inst := &Instance{ID: "i1", Service: "svc", Address: "10.0.0.1", Port: 8080, TTL: time.Minute}
	_ = r.Register(context.Background(), inst)
	time.Sleep(50 * time.Millisecond)
	oldSeen := inst.LastSeen
	_ = r.Renew(context.Background(), "svc", "i1")
	// Verify via Lookup
	list, _ := r.Lookup(context.Background(), "svc")
	if len(list) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(list))
	}
	if !list[0].LastSeen.After(oldSeen) {
		t.Error("expected LastSeen to be updated after renew")
	}
}

func TestServiceRegistry_TTLEviction(t *testing.T) {
	r := NewStaticRegistry()
	r.Start(100 * time.Millisecond)
	defer r.Stop()

	inst := &Instance{ID: "i1", Service: "svc", Address: "10.0.0.1", Port: 8080, TTL: 200 * time.Millisecond}
	_ = r.Register(context.Background(), inst)

	// Should be present initially
	list, _ := r.Lookup(context.Background(), "svc")
	if len(list) != 1 {
		t.Fatalf("expected 1 instance initially, got %d", len(list))
	}

	// Wait for TTL to expire + check interval
	time.Sleep(500 * time.Millisecond)

	list, _ = r.Lookup(context.Background(), "svc")
	if len(list) != 0 {
		t.Errorf("expected 0 instances after TTL expiry, got %d", len(list))
	}
}

func TestServiceRegistry_TTLEviction_Mutation(t *testing.T) {
	// Mutation: TTL checker does not evict expired instances
	r := NewStaticRegistry()
	r.Start(100 * time.Millisecond)
	defer r.Stop()

	inst := &Instance{ID: "i1", Service: "svc", Address: "10.0.0.1", Port: 8080, TTL: 200 * time.Millisecond}
	_ = r.Register(context.Background(), inst)

	time.Sleep(500 * time.Millisecond)
	list, _ := r.Lookup(context.Background(), "svc")
	if len(list) != 0 {
		t.Errorf("expected 0 instances after TTL expiry, got %d", len(list))
	}
}

func TestServiceRegistry_HealthyInstances(t *testing.T) {
	r := NewStaticRegistry()
	_ = r.Register(context.Background(), &Instance{ID: "i1", Service: "svc", Address: "10.0.0.1", Port: 8080})
	_ = r.Register(context.Background(), &Instance{ID: "i2", Service: "svc", Address: "10.0.0.2", Port: 8080})

	// Manually mark i2 as unhealthy in the backend to test filtering
	backend := r.backend.(*InMemoryBackend)
	_ = backend.Put(context.Background(), "svc/i2", &Instance{ID: "i2", Service: "svc", Address: "10.0.0.2", Port: 8080, Healthy: false})

	list, _ := r.HealthyInstances(context.Background(), "svc")
	if len(list) != 1 {
		t.Errorf("expected 1 healthy instance, got %d", len(list))
	}
	if list[0].ID != "i1" {
		t.Errorf("expected i1, got %s", list[0].ID)
	}
}

func TestServiceRegistry_HealthyInstances_Mutation(t *testing.T) {
	// Mutation: HealthyInstances returns all instances
	r := NewStaticRegistry()
	_ = r.Register(context.Background(), &Instance{ID: "i1", Service: "svc", Address: "10.0.0.1", Port: 8080})
	_ = r.Register(context.Background(), &Instance{ID: "i2", Service: "svc", Address: "10.0.0.2", Port: 8080})

	// Manually mark i2 as unhealthy in the backend
	backend := r.backend.(*InMemoryBackend)
	_ = backend.Put(context.Background(), "svc/i2", &Instance{ID: "i2", Service: "svc", Address: "10.0.0.2", Port: 8080, Healthy: false})

	list, _ := r.HealthyInstances(context.Background(), "svc")
	for _, i := range list {
		if !i.Healthy {
			t.Error("expected only healthy instances")
		}
	}
}

func TestServiceRegistry_MultipleServices(t *testing.T) {
	r := NewStaticRegistry()
	_ = r.Register(context.Background(), &Instance{ID: "a1", Service: "svc-a"})
	_ = r.Register(context.Background(), &Instance{ID: "b1", Service: "svc-b"})

	aList, _ := r.Lookup(context.Background(), "svc-a")
	bList, _ := r.Lookup(context.Background(), "svc-b")
	if len(aList) != 1 || aList[0].ID != "a1" {
		t.Errorf("expected svc-a to have a1, got %v", aList)
	}
	if len(bList) != 1 || bList[0].ID != "b1" {
		t.Errorf("expected svc-b to have b1, got %v", bList)
	}
}

func TestServiceRegistry_ContextCancellation(t *testing.T) {
	r := NewStaticRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	ch, _ := r.Watch(ctx, "svc")
	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected watch channel to close after context cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for watch channel to close")
	}
}
