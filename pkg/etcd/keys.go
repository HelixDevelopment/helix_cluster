// Package etcd provides a client wrapper for etcd in Helix Cluster OS.
package etcd

import "fmt"

// Namespace is the root etcd namespace for all Helix Cluster OS data. Every
// key written by the control plane lives under this prefix so the cluster can
// share an etcd with other tenants without key collisions.
const Namespace = "/clusteros"

// Sub-namespace prefixes under /clusteros. These are the six logical segments
// documented in the HXC-1076 design:
//
//	/clusteros/nodes/       — live node descriptors (keyed by node UUID)
//	/clusteros/sessions/    — client/admin sessions
//	/clusteros/scheduler/   — scheduler pool metadata
//	/clusteros/security/    — ACLs, credential records
//	/clusteros/config/      — cluster-wide configuration blobs
//	/clusteros/locks/       — distributed mutex prefixes
const (
	NamespaceNodes     = Namespace + "/nodes"
	NamespaceSessions  = Namespace + "/sessions"
	NamespaceScheduler = Namespace + "/scheduler"
	NamespaceSecurity  = Namespace + "/security"
	NamespaceConfig    = Namespace + "/config"
	NamespaceLocks     = Namespace + "/locks"
)

// NodeKey returns the canonical etcd key for a node descriptor.
// The key is /clusteros/nodes/<uuid> where uuid is the node's unique identifier.
// Every control-plane component that needs to address a node uses this function
// so that the key schema is defined once and cannot drift.
func NodeKey(uuid string) string {
	return fmt.Sprintf("%s/%s", NamespaceNodes, uuid)
}

// SessionKey returns the canonical etcd key for a session record.
// The key is /clusteros/sessions/<sessionID>.
func SessionKey(sessionID string) string {
	return fmt.Sprintf("%s/%s", NamespaceSessions, sessionID)
}

// SchedulerPoolKey returns the canonical etcd key for a scheduler pool entry.
// The key is /clusteros/scheduler/<poolID>.
func SchedulerPoolKey(poolID string) string {
	return fmt.Sprintf("%s/%s", NamespaceScheduler, poolID)
}

// SecurityKey returns the canonical etcd key for a security record.
// The key is /clusteros/security/<name>.
func SecurityKey(name string) string {
	return fmt.Sprintf("%s/%s", NamespaceSecurity, name)
}

// ConfigKey returns the canonical etcd key for a configuration blob.
// The key is /clusteros/config/<name>.
func ConfigKey(name string) string {
	return fmt.Sprintf("%s/%s", NamespaceConfig, name)
}

// LockSchedulerKey returns the canonical etcd mutex prefix for the scheduler
// distributed lock. Concurrency primitives such as Client.Lock use this prefix
// so that a single lock scope is named consistently across all schedulers.
// The key is /clusteros/locks/scheduler.
const LockSchedulerKey = NamespaceLocks + "/scheduler"

// LockKey returns a general-purpose lock key under /clusteros/locks/<name>.
// Callers that need custom lock scopes (e.g. per-node upgrade locks) use this
// rather than hand-rolling the prefix.
func LockKey(name string) string {
	return fmt.Sprintf("%s/%s", NamespaceLocks, name)
}
