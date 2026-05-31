package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/internal/gateway"
	"github.com/HelixDevelopment/helix_cluster/pkg/health"
	"github.com/stretchr/testify/suite"
)

type GatewayEndpointsSuite struct {
	IntegrationSuite
}

func (s *GatewayEndpointsSuite) TestGatewayRoutesRequestsToCorrectBackend() {
	// Start tiny backends for each route.
	schedulerBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"service":"scheduler"}`))
	}))
	defer schedulerBackend.Close()

	sessionBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"service":"session"}`))
	}))
	defer sessionBackend.Close()

	buildBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"service":"build"}`))
	}))
	defer buildBackend.Close()

	// Create gateway and override proxies.
	g, err := gateway.NewGateway()
	s.Require().NoError(err)

	for prefix, backendURL := range map[string]string{
		"/api/v1/scheduler/": schedulerBackend.URL,
		"/api/v1/session/":   sessionBackend.URL,
		"/api/v1/build/":     buildBackend.URL,
	} {
		target, err := url.Parse(backendURL)
		s.Require().NoError(err)
		g.SetProxy(prefix, httputil.NewSingleHostReverseProxy(target))
	}

	// Test scheduler route.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scheduler/jobs", nil)
	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, req)
	s.Equal(http.StatusOK, rr.Code)
	body, _ := io.ReadAll(rr.Body)
	s.Contains(string(body), "scheduler")

	// Test session route.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/session/list", nil)
	rr = httptest.NewRecorder()
	g.ServeHTTP(rr, req)
	s.Equal(http.StatusOK, rr.Code)
	body, _ = io.ReadAll(rr.Body)
	s.Contains(string(body), "session")

	// Test build route.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/build/status", nil)
	rr = httptest.NewRecorder()
	g.ServeHTTP(rr, req)
	s.Equal(http.StatusOK, rr.Code)
	body, _ = io.ReadAll(rr.Body)
	s.Contains(string(body), "build")
}

func (s *GatewayEndpointsSuite) TestHealthEndpointAggregatesAllServices() {
	// Create a composite health checker with multiple services.
	composite := health.NewCompositeChecker()
	composite.AddCheck("scheduler", func() health.Status { return health.Healthy })
	composite.AddCheck("session", func() health.Status { return health.Healthy })
	composite.AddCheck("build", func() health.Status { return health.Degraded })

	// Serve composite health via a handler mounted at /health.
	handler := health.CompositeHandler(composite)
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	s.Require().NoError(err)
	defer resp.Body.Close()

	s.Equal(http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	s.Require().NoError(err)

	var result struct {
		Status  string `json:"status"`
		Checks  []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
		Message string `json:"message"`
	}
	err = json.Unmarshal(body, &result)
	s.Require().NoError(err)

	s.Equal("degraded", result.Status)
	s.Len(result.Checks, 3)

	statusMap := make(map[string]string)
	for _, c := range result.Checks {
		statusMap[c.Name] = c.Status
	}
	s.Equal("healthy", statusMap["scheduler"])
	s.Equal("healthy", statusMap["session"])
	s.Equal("degraded", statusMap["build"])
}

func TestGatewayEndpointsSuite(t *testing.T) {
	suite.Run(t, new(GatewayEndpointsSuite))
}
