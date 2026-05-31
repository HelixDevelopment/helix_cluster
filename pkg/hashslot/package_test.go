package hashslot

import (
	"errors"
	"testing"
)

// TestCRC16_CheckVector pins CRC16 to the canonical CRC-CCITT/XMODEM check
// value: CRC16("123456789") == 0x31C3. This is the published check constant for
// the polynomial 0x1021, zero init, no reflection. Redis ships this exact value
// in its crc16.c comment.
// Mutation: change `const poly = 0x1021` to any other polynomial (e.g. 0x8005)
// or set a non-zero initial crc -> result != 0x31C3, this assertion fails.
func TestCRC16_CheckVector(t *testing.T) {
	got := CRC16([]byte("123456789"))
	if got != 0x31C3 {
		t.Fatalf("CRC16(\"123456789\") = 0x%04X, want 0x31C3", got)
	}
}

// TestCRC16_EmptyIsZero asserts the CRC of empty input is the zero initial
// value, fixing the no-final-XOR property.
// Mutation: add a final XOR (e.g. `return crc ^ 0xFFFF`) -> CRC16(nil) == 0xFFFF
// != 0, this assertion fails.
func TestCRC16_EmptyIsZero(t *testing.T) {
	if got := CRC16(nil); got != 0 {
		t.Fatalf("CRC16(nil) = 0x%04X, want 0x0000", got)
	}
}

// TestSlot_KnownVectors pins Slot() to slots computed directly from the Redis
// CRC16 algorithm (CRC16(key) % 16384) for keys with no hash tag.
// "foo" -> CRC16 0xAF96 -> 0xAF96 % 16384 = 12182
// "bar" -> CRC16 0x93C5 -> 0x93C5 % 16384 =  5061
// (verified independently against the CRC-CCITT/XMODEM reference algorithm).
// Mutation: a wrong polynomial in CRC16 changes the slot; e.g. flipping poly
// makes Slot("foo") != 12182, this assertion fails.
func TestSlot_KnownVectors(t *testing.T) {
	cases := []struct {
		key  string
		want uint16
	}{
		{"foo", 12182},
		{"bar", 5061},
	}
	for _, c := range cases {
		if got := Slot(c.key); got != c.want {
			t.Errorf("Slot(%q) = %d, want %d", c.key, got, c.want)
		}
	}
}

// TestHashTag_CoLocation is the canonical Redis hash-tag example: two distinct
// keys sharing the same {tag} MUST land in the same slot, and that slot MUST
// equal Slot of the bare tag.
// Mutation: make HashTag return its argument unchanged (ignore tags) -> the two
// keys hash to different slots, the equality assertions fail.
func TestHashTag_CoLocation(t *testing.T) {
	a := Slot("{user1000}.following")
	b := Slot("{user1000}.followers")
	if a != b {
		t.Fatalf("Slot(following)=%d != Slot(followers)=%d; hash tag not honored", a, b)
	}
	bare := Slot("user1000")
	if a != bare {
		t.Fatalf("tagged slot %d != bare tag slot %d", a, bare)
	}
	// And honoring the tag MUST actually change the slot vs hashing the whole
	// key, otherwise the test would pass even if tags were ignored.
	whole := CRC16([]byte("{user1000}.following")) % SlotCount
	if a == whole {
		t.Fatalf("tagged slot %d coincidentally equals whole-key slot; pick a stronger vector", a)
	}
}

// TestHashTag_Extraction pins HashTag's substring rules to Redis semantics.
// Mutation: returning key[open:j+1] (including braces) or mishandling the empty
// {} case breaks one of these exact-string assertions.
func TestHashTag_Extraction(t *testing.T) {
	cases := []struct {
		key, want string
	}{
		{"{user1000}.following", "user1000"}, // body between braces
		{"foo", "foo"},                       // no brace -> whole key
		{"{}foo", "{}foo"},                   // empty body -> whole key
		{"foo{bar}{zap}", "bar"},             // first non-empty body wins
		{"{{nested}", "{nested"},             // first '}' after first '{'
		{"plain{", "plain{"},                 // '{' with no '}' -> whole key
		{"a}b{c}", "c"},                      // '}' before '{' ignored, then {c}
	}
	for _, c := range cases {
		if got := HashTag(c.key); got != c.want {
			t.Errorf("HashTag(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

// TestHashTag_EmptyBracesHashWholeKey proves that "{}" hashes the entire key:
// Slot("{}foo") must equal CRC16("{}foo")%16384, NOT CRC16("")%16384.
// Mutation: treating empty {} as a valid (empty) tag -> Slot collapses to
// CRC16("")=0 -> slot 0, this assertion fails.
func TestHashTag_EmptyBracesHashWholeKey(t *testing.T) {
	got := Slot("{}foo")
	wantWhole := CRC16([]byte("{}foo")) % SlotCount
	if got != wantWhole {
		t.Fatalf("Slot(\"{}foo\") = %d, want whole-key slot %d", got, wantWhole)
	}
	if got == 0 {
		t.Fatalf("Slot(\"{}foo\") = 0, indicates empty-string was hashed (tag bug)")
	}
}

// TestOwnerOfKey_RoutesToAssignedNode proves OwnerOfKey maps a key through its
// slot to the node that owns that slot's range.
// Mutation: in OwnerOfKey, hash a constant instead of key (or off-by-one the
// slot) -> the key resolves to the wrong/owner-less node, assertion fails.
func TestOwnerOfKey_RoutesToAssignedNode(t *testing.T) {
	m := NewSlotMap()
	if err := m.Assign(0, SlotCount-1, "nodeA"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	// Reassign exactly the slot of "foo" to nodeB and verify routing follows.
	s := Slot("foo")
	if err := m.Reassign(s, "nodeB"); err != nil {
		t.Fatalf("Reassign: %v", err)
	}
	owner, ok := m.OwnerOfKey("foo")
	if !ok || owner != "nodeB" {
		t.Fatalf("OwnerOfKey(foo) = (%q,%v), want (nodeB,true)", owner, ok)
	}
	// A different key still in nodeA's range.
	other := "bar"
	if Slot(other) == s {
		other = "baz"
	}
	owner2, ok2 := m.OwnerOfKey(other)
	if !ok2 || owner2 != "nodeA" {
		t.Fatalf("OwnerOfKey(%q) = (%q,%v), want (nodeA,true)", other, owner2, ok2)
	}
}

// TestOwnerOf_UnassignedSlot asserts the unassigned sentinel is honest.
// Mutation: return owner,true unconditionally -> ok==true here, fails.
func TestOwnerOf_UnassignedSlot(t *testing.T) {
	m := NewSlotMap()
	if owner, ok := m.OwnerOf(42); ok || owner != "" {
		t.Fatalf("OwnerOf(42) on empty map = (%q,%v), want (\"\",false)", owner, ok)
	}
}

// TestRoute_MovedForReassignedSlot proves a stable reassignment produces MOVED
// to the new owner with the exact slot number, when queried from the old owner.
// Mutation: in Route, return owner for the redirect Node but Kind NoRedirect
// when owner!=local -> the kind assertion (Moved) fails.
func TestRoute_MovedForReassignedSlot(t *testing.T) {
	m := NewSlotMap()
	if err := m.Assign(0, SlotCount-1, "nodeA"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	key := "user:42"
	s := Slot(key)
	if err := m.Reassign(s, "nodeB"); err != nil {
		t.Fatalf("Reassign: %v", err)
	}
	// nodeA receives the request but no longer owns the slot.
	r, err := m.Route("nodeA", key, false)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if r.Kind != Moved {
		t.Fatalf("Route kind = %v, want MOVED", r.Kind)
	}
	if r.Node != "nodeB" {
		t.Fatalf("Route node = %q, want nodeB", r.Node)
	}
	if r.Slot != s {
		t.Fatalf("Route slot = %d, want %d", r.Slot, s)
	}
}

// TestRoute_AskForMigratingSlotMissingKey proves an in-flight migration yields
// ASK to the migration target (NOT MOVED) for a key not present on the source.
// Mutation: in Route, ignore migrating map (always NoRedirect on owner==local)
// -> kind is NoRedirect, the ASK assertion fails. Also: swapping Ask/Moved
// constants would flip the kind and fail.
func TestRoute_AskForMigratingSlotMissingKey(t *testing.T) {
	m := NewSlotMap()
	if err := m.Assign(0, SlotCount-1, "nodeA"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	key := "session:abc"
	s := Slot(key)
	if err := m.SetMigrating(s, "nodeB"); err != nil {
		t.Fatalf("SetMigrating: %v", err)
	}
	// Source nodeA still owns the slot. Key absent locally -> ASK target.
	r, err := m.Route("nodeA", key, false)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if r.Kind != Ask {
		t.Fatalf("Route kind = %v, want ASK", r.Kind)
	}
	if r.Node != "nodeB" {
		t.Fatalf("Route node = %q, want nodeB (migration target)", r.Node)
	}
	if r.Slot != s {
		t.Fatalf("Route slot = %d, want %d", r.Slot, s)
	}
}

// TestRoute_MigratingButKeyPresentServesLocally proves that during migration a
// key still present on the source is served locally (NoRedirect), not ASK'd.
// Mutation: drop the `&& !keyPresentLocally` guard -> always ASK even when
// present, NoRedirect assertion fails.
func TestRoute_MigratingButKeyPresentServesLocally(t *testing.T) {
	m := NewSlotMap()
	if err := m.Assign(0, SlotCount-1, "nodeA"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	key := "session:present"
	s := Slot(key)
	if err := m.SetMigrating(s, "nodeB"); err != nil {
		t.Fatalf("SetMigrating: %v", err)
	}
	r, err := m.Route("nodeA", key, true) // key IS present locally
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if r.Kind != NoRedirect {
		t.Fatalf("Route kind = %v, want NONE (key present on source)", r.Kind)
	}
	if r.Node != "nodeA" {
		t.Fatalf("Route node = %q, want nodeA", r.Node)
	}
}

// TestRoute_MovedTakesPrecedenceOverMigration proves that once a slot is fully
// reassigned (Reassign clears migration), routing from the old owner is a stable
// MOVED, never ASK.
// Mutation: in Reassign, drop `delete(m.migrating, slot)` -> a stale migrating
// entry could survive; but since owner!=local now, Route still returns MOVED, so
// instead we assert the migrating record is gone via MigrationTarget.
func TestRoute_MovedTakesPrecedenceOverMigration(t *testing.T) {
	m := NewSlotMap()
	if err := m.Assign(0, SlotCount-1, "nodeA"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	key := "k"
	s := Slot(key)
	if err := m.SetMigrating(s, "nodeB"); err != nil {
		t.Fatalf("SetMigrating: %v", err)
	}
	if err := m.Reassign(s, "nodeB"); err != nil {
		t.Fatalf("Reassign: %v", err)
	}
	if tgt, ok := m.MigrationTarget(s); ok {
		t.Fatalf("MigrationTarget after Reassign = (%q,true), want cleared", tgt)
	}
	r, err := m.Route("nodeA", key, false)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if r.Kind != Moved || r.Node != "nodeB" {
		t.Fatalf("Route = %v, want MOVED nodeB", r)
	}
}

// TestRoute_UnassignedSlotErrors proves routing an unassigned slot is an error,
// surfaced as ErrUnassignedSlot.
// Mutation: have Route default owner to local on "" -> no error, fails.
func TestRoute_UnassignedSlotErrors(t *testing.T) {
	m := NewSlotMap()
	_, err := m.Route("nodeA", "anything", false)
	var unassigned ErrUnassignedSlot
	if !errors.As(err, &unassigned) {
		t.Fatalf("Route on empty map err = %v, want ErrUnassignedSlot", err)
	}
}

// TestSetMigrating_RequiresSource proves you cannot migrate an unowned slot.
// Mutation: drop the `if m.owners[slot] == ""` check -> no error, fails.
func TestSetMigrating_RequiresSource(t *testing.T) {
	m := NewSlotMap()
	err := m.SetMigrating(7, "nodeB")
	var unassigned ErrUnassignedSlot
	if !errors.As(err, &unassigned) {
		t.Fatalf("SetMigrating on unowned slot err = %v, want ErrUnassignedSlot", err)
	}
}

// TestAssign_RangeAndValidation proves Assign fills the inclusive range and
// rejects inverted ranges.
// Mutation: change loop bound `s <= int(end)` to `s < int(end)` -> the last
// slot in the range is left unassigned, OwnerOf(end) assertion fails.
func TestAssign_RangeAndValidation(t *testing.T) {
	m := NewSlotMap()
	if err := m.Assign(100, 200, "nodeA"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	for _, s := range []uint16{100, 150, 200} {
		if owner, ok := m.OwnerOf(s); !ok || owner != "nodeA" {
			t.Fatalf("OwnerOf(%d) = (%q,%v), want (nodeA,true)", s, owner, ok)
		}
	}
	if _, ok := m.OwnerOf(201); ok {
		t.Fatalf("OwnerOf(201) assigned but should be outside range")
	}
	if err := m.Assign(300, 200, "nodeA"); err == nil {
		t.Fatalf("inverted range accepted, want error")
	}
}

// TestRedirect_String pins the human-readable rendering used in logs/replies.
// Mutation: change the String format -> exact-string assertion fails.
func TestRedirect_String(t *testing.T) {
	r := Redirect{Kind: Moved, Slot: 3999, Node: "nodeB"}
	if got := r.String(); got != "MOVED 3999 nodeB" {
		t.Fatalf("Redirect.String() = %q, want %q", got, "MOVED 3999 nodeB")
	}
	if got := (Redirect{Kind: Ask, Slot: 7, Node: "n"}).String(); got != "ASK 7 n" {
		t.Fatalf("ASK render = %q", got)
	}
}
