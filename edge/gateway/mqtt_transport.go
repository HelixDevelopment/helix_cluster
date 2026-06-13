package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	mqttclient "github.com/HelixDevelopment/helix_cluster/edge/mqttclient"
)

// MQTT transport adapter (HXC-1186 integration).
//
// This file plugs the real Eclipse-Paho MQTT client (edge/mqttclient) into the
// gateway behind the Transport/Conn interfaces, so an edge endpoint that
// publishes/subscribes over a real MQTT broker can address — and be addressed
// by — endpoints on any other transport (WebSocket, QUIC, loopback) with no
// special-casing in the routing core. It uses ONLY the mqttclient public API;
// it does not modify that module.
//
// Bridging model
//
// The gateway's internal model is the Envelope; the broker's model is the
// per-node dispatch/status topic carrying a WorkItem or Status (HXC-1178). The
// adapter is the codec between them:
//
//   - Inbound status (broker -> gateway): the bridge subscribes to the wildcard
//     status topic helix/edge/+/status. A Status published by any MQTT worker
//     becomes a KindStatus Envelope sourced from that node and handed to
//     Gateway.Route, which fans it to observers on any transport. This is the
//     "MQTT worker -> gateway -> WS/QUIC observer" cross-transport path.
//
//   - Inbound dispatch (broker -> gateway): BridgeNodeInbound subscribes to one
//     node's dispatch topic via a dedicated mqttclient (whose NodeID fixes the
//     topic per the public API). A WorkItem received becomes a KindDispatch
//     Envelope addressed to that node and routed.
//
//   - Outbound dispatch (gateway -> broker): RegisterNode attaches an mqttConn
//     per node id. When the gateway routes a dispatch Envelope to that node,
//     mqttConn.Send re-encodes it as a WorkItem and PublishDispatch-es it to the
//     node's broker topic, so a dispatch that arrived over WebSocket/QUIC reaches
//     an MQTT-attached worker.
//
// The application payload is carried verbatim (Envelope.Payload <-> WorkItem
// .Payload), so a worker observes the identical task bytes the dispatcher sent.

// MQTTTransport is a real MQTT bridge transport for the gateway. It owns one
// publisher/subscriber mqttclient.Client connected to a real broker (used for
// outbound publishes and the wildcard status subscription) plus, per
// inbound-dispatch node, a dedicated subscriber client.
type MQTTTransport struct {
	gw       *Gateway
	broker   string
	clientID string
	client   *mqttclient.Client // publisher + wildcard status subscriber

	mu          sync.Mutex
	outs        map[string]*mqttConn // node id -> outbound conn
	inboundSubs []*mqttclient.Client // per-node dispatch subscribers
	closed      bool
}

// NewMQTTTransport connects a real MQTT client to broker and registers it with
// the gateway. It subscribes to the wildcard status topic so any node's status
// published to the broker is bridged into the gateway. ctx bounds the connect +
// subscribe handshake. broker is an MQTT endpoint, e.g. "tcp://127.0.0.1:1883".
func NewMQTTTransport(ctx context.Context, gw *Gateway, broker, clientID string) (*MQTTTransport, error) {
	t := &MQTTTransport{
		gw:       gw,
		broker:   broker,
		clientID: clientID,
		outs:     make(map[string]*mqttConn),
	}

	cli, err := mqttclient.New(mqttclient.Config{
		Broker:   broker,
		NodeID:   clientID,
		ClientID: clientID,
	})
	if err != nil {
		return nil, fmt.Errorf("gateway/mqtt: new client: %w", err)
	}
	if err := cli.Connect(ctx); err != nil {
		return nil, fmt.Errorf("gateway/mqtt: connect: %w", err)
	}
	t.client = cli

	// Bridge inbound status (wildcard): a Status on helix/edge/<node>/status
	// becomes a KindStatus envelope sourced from <node>, fanned to observers.
	if err := cli.SubscribeStatus(ctx, mqttclient.AllStatusTopic(), func(s mqttclient.Status) error {
		_ = t.gw.Route(statusToEnvelope(s))
		return nil
	}); err != nil {
		cli.Close(time.Second)
		return nil, fmt.Errorf("gateway/mqtt: subscribe status: %w", err)
	}

	gw.AddTransport(t)
	return t, nil
}

// Name implements Transport.
func (t *MQTTTransport) Name() string { return "mqtt" }

// Client exposes the underlying bridge client (for diagnostics/tests, e.g.
// asserting broker connectivity).
func (t *MQTTTransport) Client() *mqttclient.Client { return t.client }

// BridgeNodeInbound subscribes the bridge to nodeID's dispatch topic so a
// WorkItem published there by an MQTT controller is converted to a KindDispatch
// Envelope addressed to nodeID and routed onto whatever transport that node is
// attached to. It uses a dedicated subscriber client because the mqttclient
// dispatch subscription is keyed to the client's NodeID. ctx bounds the
// connect + subscribe handshake.
func (t *MQTTTransport) BridgeNodeInbound(ctx context.Context, nodeID string) error {
	sub, err := mqttclient.New(mqttclient.Config{
		Broker:   t.broker,
		NodeID:   nodeID,
		ClientID: t.clientID + "-in-" + nodeID,
	})
	if err != nil {
		return fmt.Errorf("gateway/mqtt: new inbound client for %s: %w", nodeID, err)
	}
	if err := sub.Connect(ctx); err != nil {
		return fmt.Errorf("gateway/mqtt: connect inbound for %s: %w", nodeID, err)
	}
	if err := sub.SubscribeDispatch(ctx, func(w mqttclient.WorkItem) error {
		_ = t.gw.Route(workItemToEnvelope(nodeID, w))
		return nil
	}); err != nil {
		sub.Close(time.Second)
		return fmt.Errorf("gateway/mqtt: subscribe dispatch for %s: %w", nodeID, err)
	}

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		sub.Close(time.Second)
		return ErrClosed
	}
	t.inboundSubs = append(t.inboundSubs, sub)
	t.mu.Unlock()
	return nil
}

// RegisterNode registers an outbound bridge connection for nodeID so that
// dispatch envelopes the gateway routes to nodeID are PublishDispatch-ed to that
// node's broker topic. It is the MQTT analogue of a worker attaching. It is
// idempotent.
func (t *MQTTTransport) RegisterNode(nodeID string) (Conn, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, ErrClosed
	}
	if c, ok := t.outs[nodeID]; ok {
		t.mu.Unlock()
		return c, nil
	}
	c := &mqttConn{t: t, nodeID: nodeID}
	t.outs[nodeID] = c
	t.mu.Unlock()

	if err := t.gw.Register(c); err != nil {
		t.mu.Lock()
		delete(t.outs, nodeID)
		t.mu.Unlock()
		return nil, err
	}
	return c, nil
}

// Close releases the broker connection(s). Safe to call once.
func (t *MQTTTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	conns := make([]*mqttConn, 0, len(t.outs))
	for _, c := range t.outs {
		conns = append(conns, c)
	}
	subs := t.inboundSubs
	t.outs = make(map[string]*mqttConn)
	t.inboundSubs = nil
	t.mu.Unlock()

	for _, c := range conns {
		t.gw.Unregister(c)
	}
	for _, s := range subs {
		s.Close(time.Second)
	}
	t.client.Close(time.Second)
	return nil
}

// mqttConn is an outbound bridge connection for one node id. It implements
// Conn: Send re-encodes the routed Envelope as a broker WorkItem/Status and
// PUBLISHes it to the node's topic.
type mqttConn struct {
	t      *MQTTTransport
	nodeID string
}

// NodeID implements Conn.
func (c *mqttConn) NodeID() string { return c.nodeID }

// Send implements Conn: for a dispatch it re-publishes the work item to the
// node's broker dispatch topic (gateway -> MQTT worker). Status envelopes are
// not republished to the broker by the bridge (a status already originates from
// the broker or is destined for observers on other transports), so they are
// accepted as a no-op to avoid echoing a node's own heartbeat back at it.
func (c *mqttConn) Send(env Envelope) error {
	switch env.Kind {
	case KindDispatch:
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return c.t.client.PublishDispatch(ctx, c.nodeID, envelopeToWorkItem(env))
	case KindStatus:
		return nil
	default:
		return fmt.Errorf("gateway/mqtt: cannot publish envelope %q of kind %q", env.ID, env.Kind)
	}
}

// --- Envelope <-> broker message codecs ---

// workItemToEnvelope maps a dispatch WorkItem received on node's topic to a
// gateway dispatch Envelope addressed to that node.
func workItemToEnvelope(nodeID string, w mqttclient.WorkItem) Envelope {
	return Envelope{
		ID:       w.ID,
		Kind:     KindDispatch,
		Dest:     nodeID,
		Topic:    DispatchTopic(nodeID),
		Payload:  w.Payload,
		IssuedAt: w.IssuedAt,
	}
}

// envelopeToWorkItem maps a gateway dispatch Envelope to a broker WorkItem. The
// gateway envelope carries no work-type, so a generic non-empty "dispatch" kind
// is stamped (the broker codec requires Kind). The application payload is
// carried verbatim.
func envelopeToWorkItem(env Envelope) mqttclient.WorkItem {
	return mqttclient.WorkItem{
		ID:       env.ID,
		Kind:     "dispatch",
		Payload:  env.Payload,
		IssuedAt: env.IssuedAt,
	}
}

// statusToEnvelope maps a broker Status to a gateway status Envelope sourced
// from the publishing node. The full Status is preserved as the envelope
// payload so observers see the original health snapshot byte-for-byte.
//
// A heartbeat from the broker is unaddressed (no Dest, no Topic) so the gateway
// fans it to every observer on every transport EXCEPT the originating node —
// exactly the status fan-back semantics. Carrying a Topic here would instead
// pin routing to the source node's own conns and starve cross-transport
// observers, so it is deliberately omitted.
func statusToEnvelope(s mqttclient.Status) Envelope {
	payload, _ := json.Marshal(s)
	return Envelope{
		ID:       "status-" + s.NodeID + "-" + s.Timestamp.Format(time.RFC3339Nano),
		Kind:     KindStatus,
		Source:   s.NodeID,
		Payload:  payload,
		IssuedAt: s.Timestamp,
	}
}
