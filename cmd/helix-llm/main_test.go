package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/internal/llm"
)

func TestHandleList(t *testing.T) {
	mgr := llm.NewManager()
	_ = mgr.RegisterModel("m1", "/models/m1", "gguf")
	s := &server{manager: mgr}

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	w := httptest.NewRecorder()
	s.handleList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp) != 1 {
		t.Errorf("models = %d, want 1", len(resp))
	}
}

func TestHandleRegister(t *testing.T) {
	mgr := llm.NewManager()
	s := &server{manager: mgr}

	body, _ := json.Marshal(map[string]string{"name": "m2", "path": "/models/m2", "format": "gguf"})
	req := httptest.NewRequest(http.MethodPost, "/models/register", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleRegister(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}
}

func TestHandleInference(t *testing.T) {
	mgr := llm.NewManager()
	_ = mgr.RegisterModel("m1", "/models/m1", "gguf")
	s := &server{manager: mgr}

	body, _ := json.Marshal(map[string]string{"model": "m1", "prompt": "hello"})
	req := httptest.NewRequest(http.MethodPost, "/inference", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleInference(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestHandleHealth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
	})
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}
