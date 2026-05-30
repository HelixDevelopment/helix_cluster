package websocket

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsWebSocketRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Upgrade", "websocket")
	if !IsWebSocketRequest(req) {
		t.Error("expected WebSocket request")
	}
}
