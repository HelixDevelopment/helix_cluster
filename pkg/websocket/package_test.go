package websocket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestIsWebSocketRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	if !IsWebSocketRequest(req) {
		t.Error("expected WebSocket request")
	}
}

func TestIsWebSocketRequestMissingConnection(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Upgrade", "websocket")
	if IsWebSocketRequest(req) {
		t.Error("expected not WebSocket request without Connection: upgrade")
	}
}

func TestIsWebSocketRequestCaseInsensitive(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Upgrade", "Websocket")
	req.Header.Set("Connection", "keep-alive, Upgrade")
	if !IsWebSocketRequest(req) {
		t.Error("expected WebSocket request with mixed case")
	}
}

func TestUpgradeSuccess(t *testing.T) {
	upgrader := &Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r)
		if err != nil {
			t.Logf("upgrade error: %v", err)
			return
		}
		defer conn.Close()
		// Echo back a message
		if err := conn.WriteMessage(websocket.TextMessage, []byte("hello")); err != nil {
			t.Logf("write error: %v", err)
		}
	}))
	defer server.Close()

	// Convert http:// to ws://
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer ws.Close()

	_, msg, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if string(msg) != "hello" {
		t.Errorf("expected hello, got %s", string(msg))
	}
}

func TestUpgradeRejectsNonWebSocket(t *testing.T) {
	upgrader := &Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := upgrader.Upgrade(w, r)
		if err == nil {
			t.Error("expected error for non-WebSocket request")
		}
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("get error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestUpgradeCustomCheckOrigin(t *testing.T) {
	upgrader := &Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return r.Header.Get("X-Allowed") == "yes"
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r)
		if err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		defer conn.Close()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	// Request without X-Allowed header should fail
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("expected dial error for forbidden origin")
	}
	if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// --- Mutation Tests ---

func TestMutationUpgradeTamperedHeaders(t *testing.T) {
	upgrader := &Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mutation: tamper with request headers to remove Upgrade
		r.Header.Del("Upgrade")
		_, err := upgrader.Upgrade(w, r)
		if err == nil {
			t.Error("mutation: expected upgrade to fail after tampering headers")
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	_, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("mutation: expected dial error")
	}
}

func TestMutationIsWebSocketRequestFalsePositive(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Upgrade", "websocket")
	// Mutation: Connection header does not contain "upgrade"
	req.Header.Set("Connection", "close")
	if IsWebSocketRequest(req) {
		t.Error("mutation: expected false when Connection lacks upgrade")
	}
}
