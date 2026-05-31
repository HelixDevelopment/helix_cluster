// Package hashslot provides Redis-Cluster-compatible hash slot assignment and
// MOVED/ASK redirection for Helix Cluster OS.
//
// Redis Cluster partitions the keyspace into a fixed 16384 hash slots. A key is
// mapped to a slot by CRC16(key) % 16384, where CRC16 is the CCITT/XMODEM
// variant (polynomial 0x1021, zero initial value, no reflection, no final XOR)
// used by Redis. To let related keys co-locate on the same node, Redis supports
// hash tags: if the key contains a "{...}" substring with a non-empty body
// between the first '{' and the next '}', only that body is hashed.
//
// A SlotMap assigns each of the 16384 slots to a node. It answers OwnerOf and
// OwnerOfKey, supports reassigning slot ranges, and models in-flight slot
// migration so that a request for a key whose slot is not served locally yields
// the correct redirection: a stable MOVED to the new owner for a fully
// reassigned slot, or an ASK to the migration target for a slot currently
// migrating. The semantics mirror the Redis Cluster specification.
package hashslot

import (
	"fmt"
	"sync"
)

// SlotCount is the number of hash slots in a Redis-compatible cluster.
const SlotCount = 16384

// crc16Table is the precomputed lookup table for the CRC-CCITT (XMODEM) variant
// with polynomial 0x1021, generated once at package initialization. This is the
// exact table Redis ships in its crc16.c.
var crc16Table [256]uint16

func init() {
	const poly = 0x1021
	for i := 0; i < 256; i++ {
		crc := uint16(i) << 8
		for bit := 0; bit < 8; bit++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ poly
			} else {
				crc <<= 1
			}
		}
		crc16Table[i] = crc
	}
}

// CRC16 computes the CCITT/XMODEM CRC-16 of data (polynomial 0x1021, initial
// value 0x0000, no input/output reflection, no final XOR). This is the exact
// function Redis uses for hash-slot assignment. The standard check vector
// CRC16("123456789") == 0x31C3 holds.
func CRC16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc = (crc << 8) ^ crc16Table[byte(crc>>8)^b]
	}
	return crc
}

// HashTag extracts the Redis hash-tag substring used for slot computation. If
// key contains a '{', and somewhere after it a '}', and the substring strictly
// between them is non-empty, that substring is returned. Otherwise the whole key
// is returned. This matches Redis's keyHashTag rules exactly, including the
// edge cases of an empty "{}" body (whole key) and "{}foo" (whole key).
func HashTag(key string) string {
	open := -1
	for i := 0; i < len(key); i++ {
		if key[i] == '{' {
			open = i
			break
		}
	}
	if open < 0 {
		return key
	}
	// Find the first '}' after the '{'.
	for j := open + 1; j < len(key); j++ {
		if key[j] == '}' {
			if j == open+1 {
				// Empty {} body: hash the whole key.
				return key
			}
			return key[open+1 : j]
		}
	}
	// No '}' after '{': hash the whole key.
	return key
}

// Slot returns the hash slot in [0, SlotCount) for key, honoring hash tags:
// Slot(key) = CRC16(HashTag(key)) % SlotCount.
func Slot(key string) uint16 {
	return CRC16([]byte(HashTag(key))) % SlotCount
}

// Migration describes an in-flight slot migration. While a slot is migrating,
// the source node still owns it (and serves keys that are present locally), but
// keys not found locally must be redirected with ASK to Target so the client
// completes the operation against the node receiving the slot.
type Migration struct {
	// Slot is the slot being migrated.
	Slot uint16
	// Target is the node id receiving the slot.
	Target string
}

// RedirectKind distinguishes the two Redis Cluster redirection responses.
type RedirectKind int

const (
	// NoRedirect indicates the queried node serves the slot itself.
	NoRedirect RedirectKind = iota
	// Moved is a stable redirection: the slot has been (re)assigned to another
	// node. Clients should update their slot map and retry against Node.
	Moved
	// Ask is a one-shot redirection during migration: the slot is migrating and
	// this particular key is not present on the source, so the client should
	// retry against the migration target for this request only (without updating
	// its slot map).
	Ask
)

// String renders the redirection kind for diagnostics.
func (k RedirectKind) String() string {
	switch k {
	case NoRedirect:
		return "NONE"
	case Moved:
		return "MOVED"
	case Ask:
		return "ASK"
	default:
		return fmt.Sprintf("RedirectKind(%d)", int(k))
	}
}

// Redirect is the result of routing a key on a node that may not be the slot's
// authoritative owner. For Moved and Ask, Node is the node id to retry against
// and Slot is the affected slot, mirroring the Redis "MOVED <slot> <addr>" and
// "ASK <slot> <addr>" replies. For NoRedirect, Node is the local node.
type Redirect struct {
	Kind RedirectKind
	Slot uint16
	Node string
}

// String renders the redirection in a Redis-reply-like form, e.g.
// "MOVED 3999 nodeB" or "NONE 0 nodeA".
func (r Redirect) String() string {
	return fmt.Sprintf("%s %d %s", r.Kind, r.Slot, r.Node)
}

// ErrSlotOutOfRange is returned when a slot index is not in [0, SlotCount).
type ErrSlotOutOfRange struct {
	Slot int
}

func (e ErrSlotOutOfRange) Error() string {
	return fmt.Sprintf("hashslot: slot %d out of range [0,%d)", e.Slot, SlotCount)
}

// ErrUnassignedSlot is returned when a slot has no owner.
type ErrUnassignedSlot struct {
	Slot uint16
}

func (e ErrUnassignedSlot) Error() string {
	return fmt.Sprintf("hashslot: slot %d is not assigned to any node", e.Slot)
}

// SlotMap assigns the 16384 hash slots to nodes and tracks in-flight slot
// migrations. It is safe for concurrent use.
type SlotMap struct {
	mu sync.RWMutex
	// owners[slot] is the node id owning slot, or "" if unassigned.
	owners [SlotCount]string
	// migrating[slot] is the target node for an in-flight migration of slot,
	// present only while the slot is migrating.
	migrating map[uint16]string
}

// NewSlotMap returns an empty SlotMap with no slots assigned.
func NewSlotMap() *SlotMap {
	return &SlotMap{migrating: make(map[uint16]string)}
}

// validSlot reports whether slot is a legal index.
func validSlot(slot int) bool { return slot >= 0 && slot < SlotCount }

// Assign assigns the inclusive slot range [start, end] to node. It returns an
// error if the range is out of bounds, inverted, or node is empty.
func (m *SlotMap) Assign(start, end uint16, node string) error {
	if node == "" {
		return fmt.Errorf("hashslot: cannot assign slots to an empty node id")
	}
	if !validSlot(int(start)) {
		return ErrSlotOutOfRange{Slot: int(start)}
	}
	if !validSlot(int(end)) {
		return ErrSlotOutOfRange{Slot: int(end)}
	}
	if start > end {
		return fmt.Errorf("hashslot: inverted range [%d,%d]", start, end)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for s := int(start); s <= int(end); s++ {
		m.owners[s] = node
	}
	return nil
}

// Reassign moves a single slot to newNode as a stable assignment and clears any
// in-flight migration recorded for that slot. After Reassign the slot is fully
// owned by newNode, so routing a non-local key for it yields MOVED.
func (m *SlotMap) Reassign(slot uint16, newNode string) error {
	if newNode == "" {
		return fmt.Errorf("hashslot: cannot reassign slot to an empty node id")
	}
	if !validSlot(int(slot)) {
		return ErrSlotOutOfRange{Slot: int(slot)}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.owners[slot] = newNode
	delete(m.migrating, slot)
	return nil
}

// SetMigrating records that slot is migrating from its current owner to target.
// The current owner is unchanged (it still serves locally-present keys); only
// keys missing locally are redirected with ASK to target. Returns an error if
// the slot is currently unassigned (there is no source to migrate from) or
// target is empty.
func (m *SlotMap) SetMigrating(slot uint16, target string) error {
	if target == "" {
		return fmt.Errorf("hashslot: migration target node id cannot be empty")
	}
	if !validSlot(int(slot)) {
		return ErrSlotOutOfRange{Slot: int(slot)}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.owners[slot] == "" {
		return ErrUnassignedSlot{Slot: slot}
	}
	m.migrating[slot] = target
	return nil
}

// ClearMigrating cancels any in-flight migration recorded for slot, leaving the
// current owner in place. It is a no-op if the slot was not migrating.
func (m *SlotMap) ClearMigrating(slot uint16) error {
	if !validSlot(int(slot)) {
		return ErrSlotOutOfRange{Slot: int(slot)}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.migrating, slot)
	return nil
}

// OwnerOf returns the node id that owns slot. The returned bool is false (and
// the string empty) when the slot is unassigned.
func (m *SlotMap) OwnerOf(slot uint16) (string, bool) {
	if !validSlot(int(slot)) {
		return "", false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	owner := m.owners[slot]
	return owner, owner != ""
}

// OwnerOfKey returns the node id that owns the slot for key (hash tags honored),
// and false when that slot is unassigned.
func (m *SlotMap) OwnerOfKey(key string) (string, bool) {
	return m.OwnerOf(Slot(key))
}

// MigrationTarget returns the migration target for slot and whether the slot is
// currently migrating.
func (m *SlotMap) MigrationTarget(slot uint16) (string, bool) {
	if !validSlot(int(slot)) {
		return "", false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	target, ok := m.migrating[slot]
	return target, ok
}

// Route computes the redirection for accessing key from the perspective of
// local node. keyPresentLocally reports whether the key currently exists on the
// local node; it only matters for the ASK case during migration and is ignored
// otherwise.
//
// Semantics (matching Redis Cluster):
//   - If local owns the key's slot and the slot is NOT migrating, return
//     NoRedirect.
//   - If local owns the key's slot and the slot IS migrating: return NoRedirect
//     when the key is present locally (the source still serves it), otherwise
//     return ASK to the migration target for this single request.
//   - If local does NOT own the key's slot, return a stable MOVED to the
//     authoritative owner.
//
// It returns an error if the key's slot is unassigned.
func (m *SlotMap) Route(local, key string, keyPresentLocally bool) (Redirect, error) {
	slot := Slot(key)
	m.mu.RLock()
	defer m.mu.RUnlock()

	owner := m.owners[slot]
	if owner == "" {
		return Redirect{}, ErrUnassignedSlot{Slot: slot}
	}

	if owner != local {
		// We are not the authoritative owner: stable redirection.
		return Redirect{Kind: Moved, Slot: slot, Node: owner}, nil
	}

	// We own the slot. If it is migrating and the key is not here, ASK the
	// target; otherwise we serve it locally.
	if target, ok := m.migrating[slot]; ok && !keyPresentLocally {
		return Redirect{Kind: Ask, Slot: slot, Node: target}, nil
	}
	return Redirect{Kind: NoRedirect, Slot: slot, Node: local}, nil
}
