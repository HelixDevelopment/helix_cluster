// Package mqttclient implements a real MQTT client for Helix edge worker
// nodes. It lets an edge node receive work-dispatch messages and publish
// status/heartbeat messages over a real MQTT broker (MQTT 3.1.1, QoS1).
//
// Topic conventions (HXC-1178):
//
//	helix/edge/<node_id>/dispatch  — work items dispatched TO an edge node
//	helix/edge/<node_id>/status    — status/heartbeat published BY an edge node
//
// The package intentionally has no Linux-specific dependencies and runs
// identically on every supported OS (CLAUDE-2 parity): the MQTT transport is
// plain TCP via the maintained Eclipse Paho client.
package mqttclient

import (
	"fmt"
	"strings"
)

// Topic roots and segments. Kept as constants so encode/decode and
// construction stay in lock-step and are unit-testable without a broker.
const (
	topicRoot        = "helix/edge"
	dispatchSegment  = "dispatch"
	statusSegment    = "status"
	topicSeparator   = "/"
	singleLevelWild  = "+"
	multiLevelWild   = "#"
)

// DispatchTopic returns the topic an edge node SUBSCRIBES to in order to
// receive work-dispatch messages, e.g. helix/edge/node-7/dispatch.
func DispatchTopic(nodeID string) string {
	return strings.Join([]string{topicRoot, nodeID, dispatchSegment}, topicSeparator)
}

// StatusTopic returns the topic an edge node PUBLISHES status/heartbeat
// messages to, e.g. helix/edge/node-7/status.
func StatusTopic(nodeID string) string {
	return strings.Join([]string{topicRoot, nodeID, statusSegment}, topicSeparator)
}

// AllStatusTopic returns a wildcard topic a dispatcher/controller can
// subscribe to in order to receive status from every edge node:
// helix/edge/+/status.
func AllStatusTopic() string {
	return strings.Join([]string{topicRoot, singleLevelWild, statusSegment}, topicSeparator)
}

// AllDispatchTopic returns a wildcard topic covering every node's dispatch
// channel: helix/edge/+/dispatch.
func AllDispatchTopic() string {
	return strings.Join([]string{topicRoot, singleLevelWild, dispatchSegment}, topicSeparator)
}

// NodeIDFromTopic extracts the node id from a dispatch or status topic.
// It returns an error if the topic does not match the expected
// helix/edge/<node_id>/<segment> shape. This is the inverse of
// DispatchTopic / StatusTopic and is used by a dispatcher to learn which
// node a received status message came from.
func NodeIDFromTopic(topic string) (string, error) {
	parts := strings.Split(topic, topicSeparator)
	// Expect exactly: ["helix", "edge", "<node_id>", "<segment>"]
	if len(parts) != 4 || parts[0] != "helix" || parts[1] != "edge" {
		return "", fmt.Errorf("mqttclient: %q is not a helix/edge/<node_id>/<segment> topic", topic)
	}
	if parts[3] != dispatchSegment && parts[3] != statusSegment {
		return "", fmt.Errorf("mqttclient: topic %q has unknown segment %q", topic, parts[3])
	}
	if parts[2] == "" || parts[2] == singleLevelWild || parts[2] == multiLevelWild {
		return "", fmt.Errorf("mqttclient: topic %q has no concrete node id", topic)
	}
	return parts[2], nil
}
