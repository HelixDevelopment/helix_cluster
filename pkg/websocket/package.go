// Package websocket provides WebSocket utilities for Helix Cluster OS.
package websocket

import (
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// Upgrader upgrades HTTP connections to WebSocket connections.
type Upgrader struct {
	// CheckOrigin is a callback for origin validation. If nil, all origins are allowed.
	CheckOrigin func(r *http.Request) bool

	// HandshakeTimeout specifies the duration for the handshake to complete.
	HandshakeTimeout time.Duration

	upgrader websocket.Upgrader
}

// Upgrade upgrades an HTTP connection to a WebSocket connection.
// It returns the WebSocket connection or an error if the handshake fails.
func (u *Upgrader) Upgrade(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	u.upgrader.CheckOrigin = u.CheckOrigin
	if u.upgrader.CheckOrigin == nil {
		u.upgrader.CheckOrigin = func(r *http.Request) bool { return true }
	}
	return u.upgrader.Upgrade(w, r, nil)
}

// IsWebSocketRequest checks if the request is a WebSocket upgrade.
func IsWebSocketRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}
