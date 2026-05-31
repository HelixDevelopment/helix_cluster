package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChecker(t *testing.T) {
	c := NewChecker()
	if c.GetStatus() != Healthy {
		t.Errorf("expected healthy, got %s", c.GetStatus())
	}
	c.SetStatus(Degraded)
	if c.GetStatus() != Degraded {
		t.Errorf("expected degraded, got %s", c.GetStatus())
	}
}

func TestCompositeChecker(t *testing.T) {
	cc := NewCompositeChecker()
	cc.AddCheck("db", func() Status { return Healthy })
	cc.AddCheck("cache", func() Status { return Healthy })

	status, results := cc.Check()
	if status != Healthy {
		t.Errorf("expected overall Healthy, got %s", status)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

func TestCompositeCheckerUnhealthy(t *testing.T) {
	cc := NewCompositeChecker()
	cc.AddCheck("db", func() Status { return Healthy })
	cc.AddCheck("cache", func() Status { return Unhealthy })

	status, results := cc.Check()
	if status != Unhealthy {
		t.Errorf("expected overall Unhealthy, got %s", status)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

func TestCompositeCheckerDegraded(t *testing.T) {
	cc := NewCompositeChecker()
	cc.AddCheck("db", func() Status { return Healthy })
	cc.AddCheck("cache", func() Status { return Degraded })

	status, _ := cc.Check()
	if status != Degraded {
		t.Errorf("expected overall Degraded, got %s", status)
	}
}

func TestCompositeCheckerDegradedAndUnhealthy(t *testing.T) {
	cc := NewCompositeChecker()
	cc.AddCheck("db", func() Status { return Degraded })
	cc.AddCheck("cache", func() Status { return Unhealthy })

	status, _ := cc.Check()
	if status != Unhealthy {
		t.Errorf("expected overall Unhealthy when both degraded and unhealthy present, got %s", status)
	}
}

func TestCompositeCheckerRemoveCheck(t *testing.T) {
	cc := NewCompositeChecker()
	cc.AddCheck("db", func() Status { return Healthy })
	cc.AddCheck("cache", func() Status { return Healthy })
	cc.RemoveCheck("cache")

	status, results := cc.Check()
	if status != Healthy {
		t.Errorf("expected overall Healthy, got %s", status)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result after removal, got %d", len(results))
	}
}

func TestHandlerHealthy(t *testing.T) {
	c := NewChecker()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	Handler(c).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	var resp response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != Healthy {
		t.Errorf("expected status healthy, got %s", resp.Status)
	}
}

func TestHandlerUnhealthy(t *testing.T) {
	c := NewChecker()
	c.SetStatus(Unhealthy)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	Handler(c).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", rec.Code)
	}
	var resp response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != Unhealthy {
		t.Errorf("expected status unhealthy, got %s", resp.Status)
	}
	if resp.Message == "" {
		t.Error("expected non-empty message for unhealthy status")
	}
}

func TestCompositeHandler(t *testing.T) {
	cc := NewCompositeChecker()
	cc.AddCheck("db", func() Status { return Healthy })
	cc.AddCheck("cache", func() Status { return Degraded })

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	CompositeHandler(cc).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	var resp response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != Degraded {
		t.Errorf("expected status degraded, got %s", resp.Status)
	}
	if len(resp.Checks) != 2 {
		t.Errorf("expected 2 checks in response, got %d", len(resp.Checks))
	}
}

func TestCompositeHandlerUnhealthy(t *testing.T) {
	cc := NewCompositeChecker()
	cc.AddCheck("db", func() Status { return Unhealthy })

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	CompositeHandler(cc).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", rec.Code)
	}
}

func TestCheckFuncType(t *testing.T) {
	var fn CheckFunc = func() Status { return Healthy }
	if fn() != Healthy {
		t.Error("CheckFunc should return Healthy")
	}
}

// --- Mutation tests ---

func TestChecker_DefaultHealthy_Mutation(t *testing.T) {
	c := NewChecker()
	if c.GetStatus() != Healthy {
		t.Errorf("default status must be Healthy, got %s", c.GetStatus())
	}
}

func TestChecker_SetStatusPersists_Mutation(t *testing.T) {
	c := NewChecker()
	c.SetStatus(Unhealthy)
	if c.GetStatus() != Unhealthy {
		t.Errorf("status should be Unhealthy after SetStatus, got %s", c.GetStatus())
	}
	c.SetStatus(Degraded)
	if c.GetStatus() != Degraded {
		t.Errorf("status should be Degraded after second SetStatus, got %s", c.GetStatus())
	}
}

func TestChecker_ConcurrentAccess_Mutation(t *testing.T) {
	c := NewChecker()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			c.SetStatus(Healthy)
			c.SetStatus(Unhealthy)
		}
		close(done)
	}()
	for i := 0; i < 100; i++ {
		_ = c.GetStatus()
	}
	<-done
}

func TestCompositeChecker_ConcurrentAccess_Mutation(t *testing.T) {
	cc := NewCompositeChecker()
	cc.AddCheck("db", func() Status { return Healthy })
	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			cc.AddCheck("cache", func() Status { return Healthy })
			cc.RemoveCheck("cache")
		}
		close(done)
	}()
	for i := 0; i < 50; i++ {
		cc.Check()
	}
	<-done
}
