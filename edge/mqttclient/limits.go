package mqttclient

import (
	"fmt"
	"strings"
	"time"
)

// Security limits for the Helix edge MQTT client. These bound the resources a
// malicious or misbehaving broker/publisher can consume on an edge node, the
// same DoS / resource-exhaustion class found in the CoAP + gateway reviews.
const (
	// DefaultMaxPayloadBytes caps the size of an inbound MQTT payload the
	// client will decode. A larger published/retained message is rejected
	// (logged + dropped) BEFORE it reaches the JSON decoder, so a hostile
	// broker cannot OOM the edge node with one huge message. 256 KiB is far
	// above any legitimate Helix WorkItem/Status (a few hundred bytes) yet
	// small enough that thousands in flight stay bounded.
	DefaultMaxPayloadBytes = 256 * 1024

	// maxNodeIDLen bounds a node id used to construct topics. MQTT topics are
	// at most 65535 bytes; we keep node ids well below that so a crafted long
	// id cannot bloat topic tables or log lines.
	maxNodeIDLen = 128

	// minReconnectInterval is the floor for the auto-reconnect backoff cap. A
	// configured MaxReconnectInterval below this is raised to it so a
	// misconfiguration cannot turn paho's capped backoff into a tight
	// reconnect loop that hammers the broker/CPU on persistent failure.
	minReconnectInterval = time.Second

	// defaultMaxResumePubInFlight bounds how many stored QoS1 publishes are
	// re-sent simultaneously on reconnect. paho's default (0) is unbounded,
	// which can saturate a low-capacity edge link and spike memory after a
	// long outage. 64 keeps resume bounded while still draining quickly.
	defaultMaxResumePubInFlight = 64
)

// validNodeIDChar reports whether r is allowed in a Helix node id. We allow a
// conservative set — letters, digits, '-', '_', '.' — which covers every
// real node id (e.g. "node-7", "edge-abc", "sbc.0042") while excluding the
// MQTT wildcards '+' and '#', the level separator '/', whitespace, and control
// bytes that would let a crafted id escape its namespace or inject a wildcard
// subscription.
func validNodeIDChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '-' || r == '_' || r == '.':
		return true
	default:
		return false
	}
}

// ValidateNodeID checks that a node id is safe to embed in an MQTT topic. It
// rejects empty ids, over-length ids, and ids containing MQTT wildcards ('+',
// '#'), the topic separator ('/'), or any other disallowed byte. This is the
// single guard every topic-constructing path must pass through so a crafted
// node id can never subscribe to '#'/'+' or escape the helix/edge/<id>/...
// namespace.
func ValidateNodeID(nodeID string) error {
	if nodeID == "" {
		return fmt.Errorf("mqttclient: node id is empty")
	}
	if len(nodeID) > maxNodeIDLen {
		return fmt.Errorf("mqttclient: node id length %d exceeds max %d", len(nodeID), maxNodeIDLen)
	}
	for _, r := range nodeID {
		if !validNodeIDChar(r) {
			// Report the offending rune without leaking arbitrary bytes verbatim.
			return fmt.Errorf("mqttclient: node id %q contains illegal character %q", nodeID, r)
		}
	}
	return nil
}

// validateSubscribeTopic checks a topic a caller asks the client to subscribe
// to. It permits the legitimate single-level '+' wildcard used by the
// all-nodes status/dispatch topics but rejects the multi-level '#' wildcard
// (which would subscribe to everything under helix/edge, including other
// namespaces) and bounds the overall length.
func validateSubscribeTopic(topic string) error {
	if topic == "" {
		return fmt.Errorf("mqttclient: subscribe topic is empty")
	}
	if len(topic) > 65535 {
		return fmt.Errorf("mqttclient: subscribe topic length %d exceeds MQTT max", len(topic))
	}
	if strings.Contains(topic, multiLevelWild) {
		return fmt.Errorf("mqttclient: subscribe topic %q uses forbidden multi-level wildcard %q", topic, multiLevelWild)
	}
	if !strings.HasPrefix(topic, topicRoot+topicSeparator) {
		return fmt.Errorf("mqttclient: subscribe topic %q is outside the %q namespace", topic, topicRoot)
	}
	return nil
}
