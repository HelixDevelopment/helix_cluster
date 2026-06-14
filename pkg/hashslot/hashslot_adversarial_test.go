package hashslot

import (
	"fmt"
	"testing"
)

// independentCRC16 is a from-scratch, bit-at-a-time reimplementation of the
// CRC-16/CCITT-XMODEM algorithm (polynomial 0x1021, init 0x0000, no input or
// output reflection, no final XOR). It deliberately shares NO code with the
// production table-driven CRC16, so a mutation to the production polynomial,
// table-init, or shift direction is caught by divergence here rather than by a
// value we copied out of the same implementation under test.
func independentCRC16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// TestAdv_CRC16_PublishedVectors pins CRC16 to the canonical, externally
// published CRC-16/CCITT-XMODEM check values. These constants are documented
// independently of this codebase (the XMODEM "check" of "123456789" is the
// reference test value for poly 0x1021 / init 0x0000), so a wrong polynomial,
// non-zero init, reflection, or a final XOR all move at least one of them.
//
// Mutation that bites: change `const poly = 0x1021` in package.go init() to e.g.
// 0x8005, or change the table-init shift, or add `^ 0xFFFF` in CRC16 -> at least
// one assertion below fails with a concrete wrong checksum.
func TestAdv_CRC16_PublishedVectors(t *testing.T) {
	cases := []struct {
		in   string
		want uint16
	}{
		{"123456789", 0x31C3}, // canonical XMODEM/CCITT check constant
		{"", 0x0000},          // zero init, no final XOR
		{"A", 0x58E5},         // single byte
		{"abc", 0x9DD6},       // short ASCII
	}
	for _, c := range cases {
		if got := CRC16([]byte(c.in)); got != c.want {
			t.Errorf("CRC16(%q) = 0x%04X, want 0x%04X", c.in, got, c.want)
		}
	}
}

// TestAdv_CRC16_MatchesIndependentImpl runs CRC16 against a structurally
// different (bit-at-a-time, no shared table) reference over a broad corpus,
// including binary, unicode, empty, and long inputs. Any production CRC bug that
// the fixed vectors above miss is caught here by divergence from the oracle.
//
// Mutation that bites: any change to the production polynomial / table / update
// step makes CRC16 diverge from independentCRC16 on some input.
func TestAdv_CRC16_MatchesIndependentImpl(t *testing.T) {
	corpus := [][]byte{
		nil,
		[]byte(""),
		[]byte("a"),
		[]byte("foo"),
		[]byte("bar"),
		[]byte("user1000"),
		[]byte("Hello, World!"),
		[]byte("123456789"),
		[]byte("the quick brown fox jumps over the lazy dog"),
		[]byte("h\xc3\xa9llo"),                 // UTF-8 é
		{0x00},                                 // NUL
		{0x00, 0x01, 0x02, 0xFF},               // binary
		{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},   // all ones
		[]byte("{user1000}.following"),         // tagged whole-key
		[]byte("\x80\x81\x82\x83\x84\x85\x86"), // high bytes
	}
	// A long, varied input to exercise table coverage.
	long := make([]byte, 4096)
	for i := range long {
		long[i] = byte((i*131 + 7) & 0xFF)
	}
	corpus = append(corpus, long)

	for _, in := range corpus {
		want := independentCRC16(in)
		if got := CRC16(in); got != want {
			t.Errorf("CRC16(%q) = 0x%04X, independent oracle = 0x%04X", in, got, want)
		}
	}
}

// TestAdv_HashTag_ColocationContract is a centerpiece: every key sharing the
// same {tag} body MUST map to the SAME slot, and that slot MUST equal the slot
// of the bare tag. This is the multi-key colocation guarantee Redis Cluster
// relies on (MGET/transactions across {tag}.* keys).
//
// Mutation that bites: break the brace scan so the body is computed differently
// for ".a" vs ".b" suffixes (e.g. include trailing chars, or off-by-one the
// close index) -> the keys land in different slots and the equality fails.
func TestAdv_HashTag_ColocationContract(t *testing.T) {
	groups := []struct {
		tag  string
		keys []string
	}{
		{"user1000", []string{"{user1000}.following", "{user1000}.followers", "{user1000}", "x{user1000}y", "pre{user1000}"}},
		{"tag", []string{"key:{tag}:suffix", "{tag}", "a{tag}", "{tag}zzzzzzzz"}},
		{"42", []string{"{42}", "ord{42}er", "{42}.cart", "{42}.wishlist"}},
	}
	for _, g := range groups {
		bareSlot := Slot(g.tag)
		for _, k := range g.keys {
			if got := Slot(k); got != bareSlot {
				t.Errorf("Slot(%q) = %d, want %d (== Slot(bare tag %q)); colocation broken",
					k, got, bareSlot, g.tag)
			}
		}
		// Guard: the tag must actually be NON-trivial, i.e. tagging must change
		// the slot vs hashing a full key, otherwise this test would pass even if
		// HashTag were the identity function.
		full := "{" + g.tag + "}.aLongDistinctSuffixThatChangesTheWholeKeyHash"
		if Slot(full) == CRC16([]byte(full))%SlotCount {
			t.Errorf("for tag %q, tagged slot coincidentally equals whole-key slot; "+
				"colocation assertion would not bite", g.tag)
		}
	}
}

// TestAdv_HashTag_BraceScanRules pins the exact "first '{' .. first '}' after
// it, non-empty body" extraction against an independently derived expectation
// table. Covers empty {}, no-close, reversed order, nested, and the
// foo{}{bar} edge (first {} is empty -> whole key, NOT "bar").
//
// Mutation that bites: returning key[open:j+1] (braces included), scanning from
// the last brace, or treating empty {} as a valid empty tag changes at least one
// expected string.
func TestAdv_HashTag_BraceScanRules(t *testing.T) {
	cases := []struct {
		key, want string
	}{
		{"", ""},                             // empty key
		{"foo", "foo"},                       // no brace
		{"{user1000}.following", "user1000"}, // body
		{"key:{tag}:suffix", "tag"},          // mid-string body
		{"{}", "{}"},                         // empty body -> whole key
		{"{}foo", "{}foo"},                   // empty body then text -> whole key
		{"foo{}{bar}", "foo{}{bar}"},         // FIRST {} empty -> whole key (Redis rule)
		{"{a}{b}", "a"},                      // first non-empty body wins
		{"{{nested}", "{nested"},             // first '}' after FIRST '{'
		{"plain{", "plain{"},                 // '{' no '}' -> whole key
		{"}{", "}{"},                         // '}' before '{', no close after '{'
		{"a}b{c}", "c"},                      // leading '}' ignored, then {c}
		{"a{b}c}d", "b"},                     // first close only
		{"{ }", " "},                         // single-space body is non-empty
	}
	for _, c := range cases {
		if got := HashTag(c.key); got != c.want {
			t.Errorf("HashTag(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

// TestAdv_HashTag_EmptyBraceDoesNotCollapseToZero proves the empty-{} keys hash
// the WHOLE key, not the empty string (which would collapse them all to slot 0
// and silently co-locate unrelated keys).
//
// Mutation that bites: treat "{}" as a valid empty tag -> HashTag returns "" ->
// Slot collapses to CRC16("")%16384 == 0 for every such key; the != 0 and the
// distinctness assertions fail.
func TestAdv_HashTag_EmptyBraceDoesNotCollapseToZero(t *testing.T) {
	keys := []string{"{}foo", "{}bar", "a{}b", "foo{}{bar}", "{}"}
	seen := map[uint16]string{}
	for _, k := range keys {
		s := Slot(k)
		whole := CRC16([]byte(k)) % SlotCount
		if s != whole {
			t.Errorf("Slot(%q) = %d, want whole-key slot %d (empty {} must hash whole key)", k, s, whole)
		}
		if s == 0 && k != "" {
			// Slot 0 is legal in general, but for these specific non-empty keys a
			// 0 would be the tell-tale of empty-string hashing. None of the chosen
			// keys hash to 0 under the correct algorithm.
			t.Errorf("Slot(%q) = 0, suspicious empty-string hash (tag bug)", k)
		}
		if prev, dup := seen[s]; dup {
			t.Errorf("Slot(%q) collides with Slot(%q) at %d; empty {} likely collapsed", k, prev, s)
		}
		seen[s] = k
	}
}

// TestAdv_Slot_AlwaysInRange exercises Slot() over a large, varied key space and
// asserts every result is in [0, SlotCount). It also confirms the modulo is
// exactly SlotCount by checking, for keys whose raw CRC16 exceeds SlotCount,
// that Slot == CRC16 % SlotCount.
//
// Mutation that bites: change `% SlotCount` to `% (SlotCount-1)` (16383) or to a
// bitmask -> some key's Slot disagrees with CRC16 % 16384, or exceeds 16383.
func TestAdv_Slot_AlwaysInRange(t *testing.T) {
	var sawHigh bool
	for i := 0; i < 20000; i++ {
		key := fmt.Sprintf("k:%d:{%d}:%x", i, i*7, i)
		s := Slot(key)
		if s >= SlotCount {
			t.Fatalf("Slot(%q) = %d, out of range [0,%d)", key, s, SlotCount)
		}
		// Independent recomputation of the contract.
		want := independentCRC16([]byte(HashTag(key))) % SlotCount
		if s != want {
			t.Fatalf("Slot(%q) = %d, oracle = %d (modulo/contract mismatch)", key, s, want)
		}
		if independentCRC16([]byte(HashTag(key))) >= SlotCount {
			sawHigh = true
		}
	}
	if !sawHigh {
		t.Fatalf("test corpus never produced a raw CRC >= SlotCount; modulo not exercised")
	}
}

// TestAdv_Slot_EdgeKeysNoPanic feeds pathological keys (empty, very long,
// all-brace, unicode, binary NUL bytes) and asserts Slot stays in range and does
// not panic. Pure robustness floor.
func TestAdv_Slot_EdgeKeysNoPanic(t *testing.T) {
	long := make([]byte, 1<<16)
	for i := range long {
		long[i] = byte(i)
	}
	keys := []string{
		"",
		"{",
		"}",
		"}{",
		"{}",
		"{{{{{{",
		"}}}}}}",
		"{}{}{}{}",
		string([]byte{0x00, 0x00, 0x00}),
		"héllo wörld 🚀",
		"{🚀}.payload",
		string(long),
		"{" + string(long) + "}suffix",
	}
	for _, k := range keys {
		s := Slot(k) // must not panic
		if s >= SlotCount {
			t.Fatalf("Slot(len=%d key) = %d, out of range", len(k), s)
		}
	}
}

// TestAdv_SlotMap_FullCoverageNoGapNoOverlap assigns the entire keyspace across
// several contiguous node ranges and asserts EVERY slot 0..SlotCount-1 has
// exactly the expected owner -- no gap, no overlap, no off-by-one at range
// boundaries.
//
// Mutation that bites: change Assign's loop bound `s <= int(end)` to
// `s < int(end)` -> the last slot of each range is left to the next node (or
// unassigned at the final range), and a boundary slot's owner assertion fails.
func TestAdv_SlotMap_FullCoverageNoGapNoOverlap(t *testing.T) {
	m := NewSlotMap()
	// Three contiguous ranges covering the whole space with explicit boundaries.
	ranges := []struct {
		start, end uint16
		node       string
	}{
		{0, 5460, "nodeA"},
		{5461, 10922, "nodeB"},
		{10923, SlotCount - 1, "nodeC"},
	}
	for _, r := range ranges {
		if err := m.Assign(r.start, r.end, r.node); err != nil {
			t.Fatalf("Assign(%d,%d,%s): %v", r.start, r.end, r.node, err)
		}
	}
	ownerFor := func(slot int) string {
		switch {
		case slot <= 5460:
			return "nodeA"
		case slot <= 10922:
			return "nodeB"
		default:
			return "nodeC"
		}
	}
	for s := 0; s < SlotCount; s++ {
		owner, ok := m.OwnerOf(uint16(s))
		if !ok {
			t.Fatalf("slot %d unassigned: gap in coverage", s)
		}
		if want := ownerFor(s); owner != want {
			t.Fatalf("slot %d owner = %q, want %q (boundary/overlap error)", s, owner, want)
		}
	}
	// Explicitly hammer the exact boundary slots where off-by-one lives.
	for _, b := range []struct {
		slot uint16
		want string
	}{
		{0, "nodeA"}, {5460, "nodeA"}, {5461, "nodeB"},
		{10922, "nodeB"}, {10923, "nodeC"}, {SlotCount - 1, "nodeC"},
	} {
		if owner, _ := m.OwnerOf(b.slot); owner != b.want {
			t.Fatalf("boundary slot %d owner = %q, want %q", b.slot, owner, b.want)
		}
	}
}

// TestAdv_SlotMap_LastSlotAssigned is a focused probe for the inclusive-range
// off-by-one: assigning a single-slot range [SlotCount-1, SlotCount-1] must
// actually claim the final slot.
//
// Mutation that bites: `s <= int(end)` -> `s < int(end)` leaves this slot
// unassigned and ok==false.
func TestAdv_SlotMap_LastSlotAssigned(t *testing.T) {
	m := NewSlotMap()
	last := uint16(SlotCount - 1)
	if err := m.Assign(last, last, "edge"); err != nil {
		t.Fatalf("Assign last slot: %v", err)
	}
	owner, ok := m.OwnerOf(last)
	if !ok || owner != "edge" {
		t.Fatalf("OwnerOf(%d) = (%q,%v), want (edge,true)", last, owner, ok)
	}
}
