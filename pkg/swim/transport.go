package swim

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// Transport handles UDP send/receive for SWIM messages.
type Transport struct {
	conn     *net.UDPConn
	mu       sync.RWMutex
	handlers map[MessageType]func(*Message, net.Addr)
}

// NewTransport creates a UDP transport.
func NewTransport(bindAddr string, bindPort int) (*Transport, error) {
	addr := &net.UDPAddr{IP: net.ParseIP(bindAddr), Port: bindPort}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen udp %s:%d: %w", bindAddr, bindPort, err)
	}
	return &Transport{
		conn:     conn,
		handlers: make(map[MessageType]func(*Message, net.Addr)),
	}, nil
}

// LocalAddr returns the local address of the transport.
func (t *Transport) LocalAddr() net.Addr {
	return t.conn.LocalAddr()
}

// Send sends a message to a specific address.
func (t *Transport) Send(msg *Message, addr net.UDPAddr) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	_, err = t.conn.WriteToUDP(data, &addr)
	if err != nil {
		return fmt.Errorf("write to udp: %w", err)
	}
	return nil
}

// RegisterHandler registers a handler for a message type.
func (t *Transport) RegisterHandler(msgType MessageType, handler func(*Message, net.Addr)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.handlers[msgType] = handler
}

// Listen starts the receive loop.
func (t *Transport) Listen(ctx context.Context) error {
	buf := make([]byte, 65536)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		t.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, addr, err := t.conn.ReadFromUDP(buf)
		if err != nil {
			if opErr, ok := err.(*net.OpError); ok && opErr.Timeout() {
				continue
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return err
			}
		}

		var msg Message
		if err := json.Unmarshal(buf[:n], &msg); err != nil {
			continue
		}

		t.mu.RLock()
		handler, ok := t.handlers[msg.Type]
		t.mu.RUnlock()
		if ok {
			handler(&msg, addr)
		}
	}
}

// Close closes the transport.
func (t *Transport) Close() error {
	if t.conn != nil {
		return t.conn.Close()
	}
	return nil
}
