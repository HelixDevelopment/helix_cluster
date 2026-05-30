// Package websocket provides WebSocket utilities for Helix Cluster OS.
package websocket

import (
	"net/http"
)

// Upgrader is a placeholder WebSocket upgrader.
type Upgrader struct{}

// Upgrade upgrades an HTTP connection to WebSocket.
func (u *Upgrader) Upgrade(w http.ResponseWriter, r *http.Request) error {
	return nil
}

// IsWebSocketRequest checks if the request is a WebSocket upgrade.
func IsWebSocketRequest(r *http.Request) bool {
	return r.Header.Get("Upgrade") == "websocket"
}
