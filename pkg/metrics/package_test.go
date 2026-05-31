package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestCounter(t *testing.T) {
	c := &Counter{}
	c.Inc()
	c.Inc()
	c.Add(3)
	if c.Value() != 5 {
		t.Errorf("expected 5, got %d", c.Value())
	}
}

func TestGauge(t *testing.T) {
	g := &Gauge{}
	g.Set(10)
	if g.Value() != 10 {
		t.Errorf("expected 10, got %d", g.Value())
	}
	g.Inc()
	if g.Value() != 11 {
		t.Errorf("expected 11, got %d", g.Value())
	}
	g.Dec()
	if g.Value() != 10 {
		t.Errorf("expected 10, got %d", g.Value())
	}
	g.Add(5)
	if g.Value() != 15 {
		t.Errorf("expected 15, got %d", g.Value())
	}
	g.Add(-3)
	if g.Value() != 12 {
		t.Errorf("expected 12, got %d", g.Value())
	}
}

func TestHistogram(t *testing.T) {
	h := NewHistogram([]float64{1, 5, 10})
	h.Observe(0.5)
	h.Observe(3)
	h.Observe(7)
	h.Observe(15)

	buckets, counts, sum, count := h.Snapshot()
	if len(buckets) != 3 {
		t.Fatalf("expected 3 buckets, got %d", len(buckets))
	}
	if count != 4 {
		t.Errorf("expected count 4, got %d", count)
	}
	if sum != 25.5 {
		t.Errorf("expected sum 25.5, got %g", sum)
	}
	if counts[0] != 1 {
		t.Errorf("expected bucket[1] count 1, got %d", counts[0])
	}
	if counts[1] != 2 {
		t.Errorf("expected bucket[5] count 2, got %d", counts[1])
	}
	if counts[2] != 3 {
		t.Errorf("expected bucket[10] count 3, got %d", counts[2])
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	c := r.RegisterCounter("requests_total", "Total requests")
	g := r.RegisterGauge("active_connections", "Active connections")
	h := r.RegisterHistogram("request_duration_seconds", "Request duration", []float64{0.1, 0.5, 1})

	c.Inc()
	g.Set(5)
	h.Observe(0.2)

	if v, ok := r.Get("requests_total"); !ok {
		t.Fatal("expected requests_total to exist")
	} else if v.(*Counter).Value() != 1 {
		t.Errorf("expected counter value 1, got %d", v.(*Counter).Value())
	}

	if v, ok := r.Get("active_connections"); !ok {
		t.Fatal("expected active_connections to exist")
	} else if v.(*Gauge).Value() != 5 {
		t.Errorf("expected gauge value 5, got %d", v.(*Gauge).Value())
	}
}

func TestPrometheusHandler(t *testing.T) {
	r := NewRegistry()
	c := r.RegisterCounter("requests_total", "Total requests")
	g := r.RegisterGauge("active_connections", "Active connections")
	h := r.RegisterHistogram("request_duration_seconds", "Request duration", []float64{0.1, 0.5, 1})

	c.Add(42)
	g.Set(7)
	h.Observe(0.05)
	h.Observe(0.3)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	r.PrometheusHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "requests_total 42") {
		t.Errorf("expected requests_total 42 in output, got:\n%s", body)
	}
	if !strings.Contains(body, "active_connections 7") {
		t.Errorf("expected active_connections 7 in output, got:\n%s", body)
	}
	if !strings.Contains(body, "request_duration_seconds_bucket{le=\"0.1\"} 1") {
		t.Errorf("expected histogram bucket in output, got:\n%s", body)
	}
	if !strings.Contains(body, "request_duration_seconds_count 2") {
		t.Errorf("expected histogram count in output, got:\n%s", body)
	}
	if !strings.Contains(body, "# HELP requests_total Total requests") {
		t.Errorf("expected HELP line in output, got:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE requests_total counter") {
		t.Errorf("expected TYPE line in output, got:\n%s", body)
	}
}

func TestDefaultBuckets(t *testing.T) {
	b := DefaultBuckets()
	if len(b) != 11 {
		t.Errorf("expected 11 default buckets, got %d", len(b))
	}
	for i := 1; i < len(b); i++ {
		if b[i] <= b[i-1] {
			t.Error("default buckets must be strictly increasing")
		}
	}
}

// --- Mutation tests ---

func TestCounter_ConcurrentInc_Mutation(t *testing.T) {
	c := &Counter{}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Inc()
		}()
	}
	wg.Wait()
	if c.Value() != 100 {
		t.Errorf("expected 100 after 100 concurrent increments, got %d", c.Value())
	}
}

func TestCounter_AddNegative_Mutation(t *testing.T) {
	c := &Counter{}
	c.Add(10)
	c.Add(-3)
	if c.Value() != 7 {
		t.Errorf("expected 7 after Add(10) then Add(-3), got %d", c.Value())
	}
}

func TestCounter_ValueIsolation_Mutation(t *testing.T) {
	c1 := &Counter{}
	c2 := &Counter{}
	c1.Inc()
	if c2.Value() != 0 {
		t.Error("separate Counter instances must not share state")
	}
}

func TestGauge_ConcurrentMutation_Mutation(t *testing.T) {
	g := &Gauge{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Inc()
		}()
	}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Dec()
		}()
	}
	wg.Wait()
	if g.Value() != 0 {
		t.Errorf("expected 0 after equal inc/dec, got %d", g.Value())
	}
}

func TestHistogram_ConcurrentObserve_Mutation(t *testing.T) {
	h := NewHistogram([]float64{1, 10, 100})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(v float64) {
			defer wg.Done()
			h.Observe(v)
		}(float64(i))
	}
	wg.Wait()
	_, _, _, count := h.Snapshot()
	if count != 100 {
		t.Errorf("expected count 100 after concurrent observes, got %d", count)
	}
}

func TestRegistry_GetMissing(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("nonexistent"); ok {
		t.Error("expected Get to return false for missing metric")
	}
}
