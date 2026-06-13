package health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// mockCheck is a test double for HealthCheck.
type mockCheck struct {
	status Status
	err    error
	delay  time.Duration
}

func (m *mockCheck) Check(ctx context.Context) (Status, error) {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return Unknown, ctx.Err()
		}
	}
	return m.status, m.err
}

func TestStatusString(t *testing.T) {
	assert.Equal(t, "healthy", Healthy.String())
	assert.Equal(t, "degraded", Degraded.String())
	assert.Equal(t, "critical", Critical.String())
	assert.Equal(t, "unknown", Unknown.String())
}

func TestParseStatus(t *testing.T) {
	assert.Equal(t, Healthy, ParseStatus("healthy"))
	assert.Equal(t, Degraded, ParseStatus("degraded"))
	assert.Equal(t, Critical, ParseStatus("critical"))
	assert.Equal(t, Unknown, ParseStatus("unknown"))
	assert.Equal(t, Unknown, ParseStatus("invalid"))
}

func TestAggregator_RegisterAndRunChecks(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheck("svc-a", &mockCheck{status: Healthy})
	agg.RegisterCheck("svc-b", &mockCheck{status: Degraded})

	agg.RunChecks(context.Background())

	assert.Equal(t, Degraded, agg.GetStatus())

	svcA, ok := agg.GetServiceStatus("svc-a")
	require.True(t, ok)
	assert.Equal(t, Healthy, svcA.Status)

	svcB, ok := agg.GetServiceStatus("svc-b")
	require.True(t, ok)
	assert.Equal(t, Degraded, svcB.Status)
}

func TestAggregator_UnregisterCheck(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheck("svc-a", &mockCheck{status: Critical})
	agg.RegisterCheck("svc-b", &mockCheck{status: Healthy})
	agg.RunChecks(context.Background())

	assert.Equal(t, Critical, agg.GetStatus())

	agg.UnregisterCheck("svc-a")
	agg.RunChecks(context.Background())

	assert.Equal(t, Healthy, agg.GetStatus())
}

func TestAggregator_ConcurrentChecks(t *testing.T) {
	agg := NewAggregator()
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("svc-%d", i)
		agg.RegisterCheck(name, &mockCheck{status: Healthy, delay: 10 * time.Millisecond})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	agg.RunChecks(ctx)
	assert.Equal(t, Healthy, agg.GetStatus())
	assert.Len(t, agg.GetAllStatuses(), 50)
}

func TestAggregator_StatusAggregation(t *testing.T) {
	tests := []struct {
		name     string
		checks   map[string]Status
		expected Status
	}{
		{"all healthy", map[string]Status{"a": Healthy, "b": Healthy}, Healthy},
		{"one degraded", map[string]Status{"a": Healthy, "b": Degraded}, Degraded},
		{"one critical", map[string]Status{"a": Healthy, "b": Critical}, Critical},
		{"mixed", map[string]Status{"a": Degraded, "b": Critical}, Critical},
		{"unknown only", map[string]Status{"a": Unknown}, Unknown},
		{"healthy and unknown", map[string]Status{"a": Healthy, "b": Unknown}, Unknown},
		{"degraded and unknown", map[string]Status{"a": Degraded, "b": Unknown}, Degraded},
		{"no checks", map[string]Status{}, Unknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agg := NewAggregator()
			for n, s := range tt.checks {
				agg.RegisterCheck(n, &mockCheck{status: s})
			}
			agg.RunChecks(context.Background())
			assert.Equal(t, tt.expected, agg.GetStatus())
		})
	}
}

func TestAggregator_ThresholdStatusTransitions(t *testing.T) {
	agg := NewAggregator()

	agg.RegisterCheck("svc", &mockCheck{status: Healthy})
	agg.RunChecks(context.Background())
	assert.Equal(t, Healthy, agg.GetStatus())

	agg.RegisterCheck("svc", &mockCheck{status: Degraded})
	agg.RunChecks(context.Background())
	assert.Equal(t, Degraded, agg.GetStatus())

	agg.RegisterCheck("svc", &mockCheck{status: Critical})
	agg.RunChecks(context.Background())
	assert.Equal(t, Critical, agg.GetStatus())

	agg.RegisterCheck("svc", &mockCheck{status: Healthy})
	agg.RunChecks(context.Background())
	assert.Equal(t, Healthy, agg.GetStatus())
}

func TestAggregator_StatusWithDetails(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheck("svc-a", &mockCheck{status: Healthy})
	agg.RegisterCheck("svc-b", &mockCheck{status: Critical, err: errors.New("connection refused")})
	agg.RunChecks(context.Background())

	st, details := agg.StatusWithDetails()
	assert.Equal(t, Critical, st)
	assert.Equal(t, "healthy", details["svc-a"])
	assert.Contains(t, details["svc-b"], "critical")
	assert.Contains(t, details["svc-b"], "connection refused")
}

func TestAggregator_GetAllStatuses_Copy(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheck("svc", &mockCheck{status: Healthy})
	agg.RunChecks(context.Background())

	all := agg.GetAllStatuses()
	all["svc"] = ServiceStatus{Name: "svc", Status: Critical}

	svc, _ := agg.GetServiceStatus("svc")
	assert.Equal(t, Healthy, svc.Status)
}

func TestHTTPCheck_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	check := &HTTPCheck{URL: ts.URL, Timeout: 2 * time.Second}
	st, err := check.Check(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Healthy, st)
}

func TestHTTPCheck_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	check := &HTTPCheck{URL: ts.URL, Timeout: 2 * time.Second}
	st, err := check.Check(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Critical, st)
}

func TestHTTPCheck_CustomThreshold(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()

	check := &HTTPCheck{
		URL:     ts.URL,
		Timeout: 2 * time.Second,
		StatusThreshold: map[int]Status{
			http.StatusTooManyRequests: Degraded,
		},
	}
	st, err := check.Check(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Degraded, st)
}

func TestCheckFunc(t *testing.T) {
	cf := CheckFunc(func(ctx context.Context) (Status, error) {
		return Degraded, errors.New("something off")
	})
	st, err := cf.Check(context.Background())
	assert.Equal(t, Degraded, st)
	assert.EqualError(t, err, "something off")
}

func TestServer_Check_Overall(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheck("svc", &mockCheck{status: Healthy})
	agg.RunChecks(context.Background())

	srv := NewServer(agg)
	resp, err := srv.Check(context.Background(), &helixv1.CheckRequest{})
	require.NoError(t, err)
	assert.Equal(t, "healthy", resp.Status)
}

func TestServer_Check_Service(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheck("svc-a", &mockCheck{status: Healthy})
	agg.RegisterCheck("svc-b", &mockCheck{status: Critical})
	agg.RunChecks(context.Background())

	srv := NewServer(agg)
	resp, err := srv.Check(context.Background(), &helixv1.CheckRequest{Service: "svc-b"})
	require.NoError(t, err)
	assert.Equal(t, "critical", resp.Status)
	assert.Contains(t, resp.Details, "svc-b")
}

func TestServer_Check_ServiceNotFound(t *testing.T) {
	agg := NewAggregator()
	srv := NewServer(agg)
	_, err := srv.Check(context.Background(), &helixv1.CheckRequest{Service: "missing"})
	require.Error(t, err)
}

func TestServer_ReportHealth(t *testing.T) {
	agg := NewAggregator()
	srv := NewServer(agg)

	resp, err := srv.ReportHealth(context.Background(), &helixv1.ReportHealthRequest{
		NodeId: "node-1",
	})
	require.NoError(t, err)
	assert.True(t, resp.Accepted)
}

// Ensure Server implements the generated interface.
var _ helixv1.HealthServiceServer = (*Server)(nil)

// Ensure stream server interface compatibility.
var _ grpc.ServerStreamingServer[helixv1.HealthEvent] = (nil)
