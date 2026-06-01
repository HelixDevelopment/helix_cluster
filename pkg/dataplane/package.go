// Package dataplane provides ZeroMQ-based message patterns for internal
// Helix Cluster data planes.
//
// Two patterns are implemented:
//
//  1. ROUTER/DEALER correlated request-reply: a Distributor (ROUTER socket)
//     sends tasks tagged with a unique correlation-id to worker DEALER sockets
//     and matches correlated replies back to the originating request.
//
//  2. PUSH/PULL fair-queue log pipeline: a LogPipeline (PUSH socket) sends
//     log messages to multiple PULL worker sockets with ZMQ's built-in
//     fair-queue load-balancing, together with an application-level ack
//     channel that guarantees at-least-once delivery tracking.
//
// All sockets are created with private zmq.Context instances and use
// loopback addresses (inproc:// or tcp://127.0.0.1) to avoid external
// network dependencies during testing.
//
// NOTE: libzmq trips Go's race detector. Do NOT run tests with -race.
package dataplane
