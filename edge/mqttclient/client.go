package mqttclient

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// QoS1 is the at-least-once delivery quality of service used for all Helix
// edge dispatch and status traffic. Work items and heartbeats must not be
// silently dropped, so QoS0 is never used.
const QoS1 byte = 1

// Config configures a Client. Only Broker and NodeID are required.
type Config struct {
	// Broker is the MQTT broker endpoint, e.g. "tcp://127.0.0.1:1883".
	Broker string
	// NodeID is this edge node's identity; it determines the dispatch and
	// status topics.
	NodeID string
	// ClientID is the MQTT client id. If empty, derived from NodeID.
	ClientID string
	// Username / Password are optional broker credentials.
	Username string
	Password string
	// KeepAlive is the MQTT keep-alive interval. Default 30s.
	KeepAlive time.Duration
	// ConnectTimeout bounds the initial connect. Default 10s.
	ConnectTimeout time.Duration
	// MaxReconnectInterval caps the auto-reconnect backoff. Default 30s.
	MaxReconnectInterval time.Duration
	// OnConnect, if set, is called every time the client (re)connects. It is
	// the right place to (re)subscribe so subscriptions survive reconnects.
	OnConnect func(*Client)
	// OnConnectionLost, if set, is called when the connection drops.
	OnConnectionLost func(error)
	// Logger, if set, receives human-readable lifecycle messages.
	Logger func(string)
}

func (c *Config) withDefaults() {
	if c.KeepAlive == 0 {
		c.KeepAlive = 30 * time.Second
	}
	if c.ConnectTimeout == 0 {
		c.ConnectTimeout = 10 * time.Second
	}
	if c.MaxReconnectInterval == 0 {
		c.MaxReconnectInterval = 30 * time.Second
	}
	if c.ClientID == "" {
		c.ClientID = "helix-edge-" + c.NodeID
	}
}

// DispatchHandler is invoked for every work item received on the node's
// dispatch topic. A non-nil error is logged but does not stop delivery.
type DispatchHandler func(WorkItem) error

// StatusHandler is invoked (on a dispatcher/controller) for every status
// message received on a subscribed status topic.
type StatusHandler func(Status) error

// Client is a thin, Helix-aware wrapper over the Eclipse Paho MQTT client.
// It provides connect-with-reconnect, dispatch subscription, QoS1 status
// publishing, and graceful close. It is safe for concurrent use.
type Client struct {
	cfg  Config
	mqtt paho.Client

	mu      sync.Mutex
	closed  bool
	logfn   func(string)
}

// New constructs a Client from cfg. It does not connect; call Connect.
func New(cfg Config) (*Client, error) {
	if cfg.Broker == "" {
		return nil, errors.New("mqttclient: Config.Broker is required")
	}
	if cfg.NodeID == "" {
		return nil, errors.New("mqttclient: Config.NodeID is required")
	}
	cfg.withDefaults()

	c := &Client{cfg: cfg, logfn: cfg.Logger}

	opts := paho.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(cfg.ClientID).
		SetKeepAlive(cfg.KeepAlive).
		SetConnectTimeout(cfg.ConnectTimeout).
		SetAutoReconnect(true).
		SetMaxReconnectInterval(cfg.MaxReconnectInterval).
		SetCleanSession(true).
		SetOrderMatters(false)

	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
		opts.SetPassword(cfg.Password)
	}

	opts.SetOnConnectHandler(func(_ paho.Client) {
		c.log("connected to broker " + cfg.Broker)
		if cfg.OnConnect != nil {
			cfg.OnConnect(c)
		}
	})
	opts.SetConnectionLostHandler(func(_ paho.Client, err error) {
		c.log("connection lost: " + errString(err))
		if cfg.OnConnectionLost != nil {
			cfg.OnConnectionLost(err)
		}
	})
	opts.SetReconnectingHandler(func(_ paho.Client, _ *paho.ClientOptions) {
		c.log("reconnecting to broker " + cfg.Broker)
	})

	c.mqtt = paho.NewClient(opts)
	return c, nil
}

// NodeID returns the configured node id.
func (c *Client) NodeID() string { return c.cfg.NodeID }

// Connect establishes the broker connection, honoring ctx for cancellation
// of the initial connect attempt. Auto-reconnect (per Config) keeps the
// connection alive afterward.
func (c *Client) Connect(ctx context.Context) error {
	tok := c.mqtt.Connect()
	if !waitToken(ctx, tok) {
		return fmt.Errorf("mqttclient: connect to %s timed out/cancelled", c.cfg.Broker)
	}
	if err := tok.Error(); err != nil {
		return fmt.Errorf("mqttclient: connect to %s: %w", c.cfg.Broker, err)
	}
	return nil
}

// IsConnected reports whether the underlying client currently has a live
// broker connection.
func (c *Client) IsConnected() bool { return c.mqtt.IsConnected() }

// SubscribeDispatch subscribes this node's dispatch topic at QoS1 and routes
// each received work item to handler. Decode failures are logged and skipped
// (a poison message must not stall the subscription).
func (c *Client) SubscribeDispatch(ctx context.Context, handler DispatchHandler) error {
	if handler == nil {
		return errors.New("mqttclient: SubscribeDispatch requires a handler")
	}
	topic := DispatchTopic(c.cfg.NodeID)
	cb := func(_ paho.Client, m paho.Message) {
		item, err := DecodeWorkItem(m.Payload())
		if err != nil {
			c.log("dropping malformed dispatch on " + m.Topic() + ": " + errString(err))
			return
		}
		if err := handler(item); err != nil {
			c.log("dispatch handler error for " + item.ID + ": " + errString(err))
		}
	}
	tok := c.mqtt.Subscribe(topic, QoS1, cb)
	if !waitToken(ctx, tok) {
		return fmt.Errorf("mqttclient: subscribe %s timed out/cancelled", topic)
	}
	if err := tok.Error(); err != nil {
		return fmt.Errorf("mqttclient: subscribe %s: %w", topic, err)
	}
	c.log("subscribed dispatch topic " + topic)
	return nil
}

// SubscribeStatus subscribes to a status topic (e.g. AllStatusTopic() for a
// dispatcher, or StatusTopic(node) for a specific node) at QoS1 and routes
// each received status to handler.
func (c *Client) SubscribeStatus(ctx context.Context, topic string, handler StatusHandler) error {
	if handler == nil {
		return errors.New("mqttclient: SubscribeStatus requires a handler")
	}
	cb := func(_ paho.Client, m paho.Message) {
		st, err := DecodeStatus(m.Payload())
		if err != nil {
			c.log("dropping malformed status on " + m.Topic() + ": " + errString(err))
			return
		}
		if err := handler(st); err != nil {
			c.log("status handler error for " + st.NodeID + ": " + errString(err))
		}
	}
	tok := c.mqtt.Subscribe(topic, QoS1, cb)
	if !waitToken(ctx, tok) {
		return fmt.Errorf("mqttclient: subscribe %s timed out/cancelled", topic)
	}
	if err := tok.Error(); err != nil {
		return fmt.Errorf("mqttclient: subscribe %s: %w", topic, err)
	}
	c.log("subscribed status topic " + topic)
	return nil
}

// PublishStatus publishes a heartbeat/status for this node at QoS1 to its
// status topic. The status's NodeID is forced to this client's node id so a
// node can only speak for itself.
func (c *Client) PublishStatus(ctx context.Context, s Status) error {
	s.NodeID = c.cfg.NodeID
	if s.Timestamp.IsZero() {
		s.Timestamp = time.Now().UTC()
	}
	payload, err := EncodeStatus(s)
	if err != nil {
		return err
	}
	topic := StatusTopic(c.cfg.NodeID)
	tok := c.mqtt.Publish(topic, QoS1, false, payload)
	if !waitToken(ctx, tok) {
		return fmt.Errorf("mqttclient: publish status to %s timed out/cancelled", topic)
	}
	if err := tok.Error(); err != nil {
		return fmt.Errorf("mqttclient: publish status to %s: %w", topic, err)
	}
	return nil
}

// PublishDispatch publishes a work item to targetNodeID's dispatch topic at
// QoS1. This is the dispatcher-side counterpart to SubscribeDispatch.
func (c *Client) PublishDispatch(ctx context.Context, targetNodeID string, w WorkItem) error {
	if targetNodeID == "" {
		return errors.New("mqttclient: PublishDispatch requires a target node id")
	}
	if w.IssuedAt.IsZero() {
		w.IssuedAt = time.Now().UTC()
	}
	payload, err := EncodeWorkItem(w)
	if err != nil {
		return err
	}
	topic := DispatchTopic(targetNodeID)
	tok := c.mqtt.Publish(topic, QoS1, false, payload)
	if !waitToken(ctx, tok) {
		return fmt.Errorf("mqttclient: publish dispatch to %s timed out/cancelled", topic)
	}
	if err := tok.Error(); err != nil {
		return fmt.Errorf("mqttclient: publish dispatch to %s: %w", topic, err)
	}
	return nil
}

// Close gracefully disconnects from the broker, allowing up to quiesce for
// in-flight messages to flush. It is idempotent.
func (c *Client) Close(quiesce time.Duration) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()

	if quiesce <= 0 {
		quiesce = 250 * time.Millisecond
	}
	ms := uint(quiesce.Milliseconds())
	c.mqtt.Disconnect(ms)
	c.log("disconnected from broker " + c.cfg.Broker)
}

func (c *Client) log(msg string) {
	if c.logfn != nil {
		c.logfn(msg)
	}
}

// waitToken blocks until the Paho token completes or ctx is done. It returns
// true if the token completed, false on ctx cancellation/timeout.
func waitToken(ctx context.Context, tok paho.Token) bool {
	select {
	case <-tok.Done():
		return true
	case <-ctx.Done():
		return false
	}
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
