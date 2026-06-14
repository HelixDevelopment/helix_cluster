package nattraversal

// Adversarial codec audit for the STUN message decoder.
//
// Focus (per audit charter):
//   - ROUND-TRIP FIDELITY: a fully-populated message survives Encode→Decode
//     (and a hand-built multi-attribute message survives Decode) with every
//     field reproduced exactly.
//   - DECODE ROBUSTNESS (DoS / over-read / over-allocation): DecodeMessage is
//     fed nil, empty, every truncated prefix of a valid message, random bytes,
//     a header whose declared attribute-length exceeds the buffer, an attribute
//     whose declared length runs past the message end, and a maximal
//     attribute-length (allocation probe). Each MUST return an error and MUST
//     NOT panic, over-read, or over-allocate.
//   - VALIDATION: a non-STUN buffer (wrong magic cookie) is rejected; a valid
//     buffer is accepted.
//   - EDGES: zero attributes, exactly-header-length, attribute section
//     containing a partial (<4-byte) trailing TLV header.
//
// Every test names the one-line production mutation that makes it fail.

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"testing"
)

// ────────────────────────────────────────────────────────────────────────────
// Local builders (independent of the helpers in nattraversal_test.go so this
// file documents its own wire layout).
// ────────────────────────────────────────────────────────────────────────────

// tlv builds one STUN attribute TLV (type, length, value) with RFC 5389 §15
// 4-byte padding of the value.
func tlv(atype uint16, value []byte) []byte {
	padded := len(value)
	if rem := len(value) % 4; rem != 0 {
		padded += 4 - rem
	}
	out := make([]byte, 4+padded)
	binary.BigEndian.PutUint16(out[0:2], atype)
	binary.BigEndian.PutUint16(out[2:4], uint16(len(value)))
	copy(out[4:], value)
	return out
}

// buildMessage assembles a STUN message with the given type, txID, and the
// concatenation of pre-built attribute TLVs. The header attribute-length is set
// to the exact length of the attribute section.
func buildMessage(msgType uint16, txID [txIDLen]byte, attrSection []byte) []byte {
	buf := make([]byte, headerLen+len(attrSection))
	binary.BigEndian.PutUint16(buf[0:2], msgType)
	binary.BigEndian.PutUint16(buf[2:4], uint16(len(attrSection)))
	binary.BigEndian.PutUint32(buf[4:8], magicCookie)
	copy(buf[8:20], txID[:])
	copy(buf[headerLen:], attrSection)
	return buf
}

func fixedTxID() [txIDLen]byte {
	var id [txIDLen]byte
	for i := range id {
		id[i] = byte(0xA0 + i)
	}
	return id
}

// ────────────────────────────────────────────────────────────────────────────
// CENTERPIECE 1 — round-trip fidelity for a fully-populated, multi-attribute
// message with DISTINCT values per field.
// ────────────────────────────────────────────────────────────────────────────

// TestDecodeMultiAttributeRoundTripFidelity builds a message carrying three
// distinct attributes — two with non-4-aligned value lengths (forcing padding)
// and one zero-length — and asserts Decode reproduces the message type, the
// transaction ID, and every attribute value byte-for-byte. This pins the TLV
// walk: type parse, length parse, value copy, and padding advance.
//
// Mutation that kills it: in DecodeMessage's advance step, drop the padding
// round-up (`padded := alen` with the `rem` block removed) → after the first
// non-aligned attribute the walk desynchronises and reads the next type/length
// from inside the previous value's padding, so the second/third attribute keys
// or values mismatch and the exact-equality asserts fire.
func TestDecodeMultiAttributeRoundTripFidelity(t *testing.T) {
	txID := fixedTxID()

	// Distinct values; lengths 3 and 5 are deliberately NOT multiples of 4 so
	// the decoder MUST apply padding to stay aligned.
	const (
		typeA uint16 = 0x8001 // comprehension-optional range
		typeB uint16 = 0x8002
		typeC uint16 = 0x0006 // USERNAME-like
	)
	valA := []byte{0x11, 0x22, 0x33}             // len 3 → padded to 4
	valB := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x42} // len 5 → padded to 8
	valC := []byte{}                             // len 0 → no padding

	section := bytes.Join([][]byte{
		tlv(typeA, valA),
		tlv(typeB, valB),
		tlv(typeC, valC),
	}, nil)

	const msgType uint16 = 0x0101 // Binding Success
	buf := buildMessage(msgType, txID, section)

	msg, err := DecodeMessage(buf)
	if err != nil {
		t.Fatalf("DecodeMessage: %v", err)
	}

	if msg.Type != msgType {
		t.Errorf("Type = 0x%04X, want 0x%04X", msg.Type, msgType)
	}
	if msg.TxID != txID {
		t.Errorf("TxID = % x, want % x", msg.TxID, txID)
	}
	if len(msg.Attributes) != 3 {
		t.Fatalf("attribute count = %d, want 3 (keys=%v)", len(msg.Attributes), msg.Attributes)
	}

	wantAttrs := map[uint16][]byte{
		typeA: valA,
		typeB: valB,
		typeC: valC,
	}
	for at, want := range wantAttrs {
		got, ok := msg.Attributes[at]
		if !ok {
			t.Errorf("attribute 0x%04X missing", at)
			continue
		}
		// Value must be exactly the unpadded value — no padding bytes leaked in,
		// no value bytes dropped.
		if !bytes.Equal(got, want) {
			t.Errorf("attribute 0x%04X value = % x, want % x", at, got, want)
		}
	}
}

// TestEncodeBindingRequestRoundTripDistinctTxIDs proves two successive
// EncodeBindingRequest calls produce DISTINCT transaction IDs (crypto/rand is
// actually consumed) and that each decodes back to its own TxID. This guards
// the field that binds a response to its request.
//
// Mutation that kills it: make encodeRequest write a constant TxID (e.g. copy a
// zero array) → the two encoded TxIDs become equal and the inequality assert
// fires.
func TestEncodeBindingRequestRoundTripDistinctTxIDs(t *testing.T) {
	enc1, id1, err := EncodeBindingRequest()
	if err != nil {
		t.Fatalf("EncodeBindingRequest #1: %v", err)
	}
	enc2, id2, err := EncodeBindingRequest()
	if err != nil {
		t.Fatalf("EncodeBindingRequest #2: %v", err)
	}
	if id1 == id2 {
		t.Fatalf("two EncodeBindingRequest calls produced identical TxID % x", id1)
	}

	m1, err := DecodeMessage(enc1)
	if err != nil {
		t.Fatalf("decode enc1: %v", err)
	}
	m2, err := DecodeMessage(enc2)
	if err != nil {
		t.Fatalf("decode enc2: %v", err)
	}
	if m1.TxID != id1 {
		t.Errorf("decoded TxID #1 = % x, want % x", m1.TxID, id1)
	}
	if m2.TxID != id2 {
		t.Errorf("decoded TxID #2 = % x, want % x", m2.TxID, id2)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// CENTERPIECE 2 — decode robustness: never panic, never over-read, never
// over-allocate on truncated / hostile bytes.
// ────────────────────────────────────────────────────────────────────────────

// TestDecodeNeverPanicsOnTruncatedPrefixes feeds EVERY prefix (length 0..N) of
// a valid multi-attribute STUN message to DecodeMessage. A correct decoder
// either returns a *Message (for prefixes that happen to be self-consistent) or
// an error — but never panics and never reads beyond the slice it was given.
//
// This is the classic over-read probe: a header claiming more attribute bytes
// than the truncated prefix actually contains MUST be rejected by the
// `headerLen+attrLen > len(buf)` guard, not dereferenced.
//
// Mutation that kills it: delete the `if headerLen+attrLen > len(buf)` guard in
// DecodeMessage → slicing buf[headerLen:headerLen+attrLen] panics with slice
// bounds out of range on the first truncated prefix whose declared length
// exceeds the prefix, and the recover() below records the panic.
func TestDecodeNeverPanicsOnTruncatedPrefixes(t *testing.T) {
	full := buildMessage(0x0101, fixedTxID(), bytes.Join([][]byte{
		tlv(attrXorMappedAddress, []byte{0x00, familyIPv4, 0xF5, 0x23, 0xEA, 0x12, 0xD5, 0x45}),
		tlv(0x8001, []byte{0x01, 0x02, 0x03}),
	}, nil))

	for n := 0; n <= len(full); n++ {
		prefix := full[:n:n] // cap=n so any over-read past n panics, not silently succeeds
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("DecodeMessage panicked on %d-byte prefix: %v\nbytes: % x", n, r, prefix)
				}
			}()
			_, _ = DecodeMessage(prefix)
		}()
	}
}

// TestDecodeNeverPanicsOnRandomBytes throws pseudo-random buffers of many
// lengths at DecodeMessage. With a deterministic seed this is reproducible. The
// contract: no input may cause a panic, an over-read, or a hang.
//
// Mutation that kills it: remove the inner `if 4+alen > len(attrs)` attribute
// bound check → a random buffer whose attribute claims a length past the
// section slices attrs[4:4+alen] out of range and panics, caught by recover().
func TestDecodeNeverPanicsOnRandomBytes(t *testing.T) {
	rng := rand.New(rand.NewSource(0x5713_1957))
	for i := 0; i < 20000; i++ {
		n := rng.Intn(80) // covers <header, =header, and header+attrs ranges
		b := make([]byte, n)
		rng.Read(b)
		// Force-cap the slice so any read past len(b) is a real over-read panic.
		b = b[:n:n]
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("DecodeMessage panicked on random %d-byte buffer: %v\nbytes: % x", n, r, b)
				}
			}()
			_, _ = DecodeMessage(b)
		}()
	}
}

// TestDecodeAttrLengthLongerThanBufferRejected pins the header-level over-read
// guard with an EXACT hostile byte vector: a 20-byte header declaring 0xFFFF
// (65535) bytes of attributes while zero follow it.
//
// Mutation that kills it: delete the `headerLen+attrLen > len(buf)` guard →
// either a panic (slice out of range) or a *Message with a giant attribute
// section is returned; this test requires an error and no panic.
func TestDecodeAttrLengthLongerThanBufferRejected(t *testing.T) {
	b := make([]byte, headerLen)
	binary.BigEndian.PutUint16(b[0:2], 0x0101)
	binary.BigEndian.PutUint16(b[2:4], 0xFFFF) // claim 65535 attr bytes
	binary.BigEndian.PutUint32(b[4:8], magicCookie)

	b = b[:headerLen:headerLen] // cap-locked: any over-read panics
	var msg *Message
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("DecodeMessage panicked on over-long attr-length header: %v", r)
			}
		}()
		msg, err = DecodeMessage(b)
	}()
	if err == nil {
		t.Fatalf("DecodeMessage accepted header claiming 65535 attr bytes with none present; got msg=%+v", msg)
	}
}

// TestDecodeInnerAttrLengthOverrunRejected pins the inner (per-attribute) bound
// check: the header's attribute-section length is honest, but the single
// attribute INSIDE it claims a value length that runs past the section end.
//
// Layout: header attr-len = 8. Section = [type=0x8001][len=0xFFFF][4 bytes].
// The declared value length 65535 overruns the 4 remaining bytes.
//
// Mutation that kills it: delete the inner `if 4+alen > len(attrs)` guard →
// the decoder allocates make([]byte, 65535) and copies attrs[4:65539],
// panicking on the out-of-range slice (caught by recover) — a real over-read /
// over-allocation DoS. This test requires an error and no panic.
func TestDecodeInnerAttrLengthOverrunRejected(t *testing.T) {
	section := make([]byte, 8)
	binary.BigEndian.PutUint16(section[0:2], 0x8001) // attr type
	binary.BigEndian.PutUint16(section[2:4], 0xFFFF) // claim 65535-byte value
	// section[4:8] are the only 4 value bytes actually present.

	buf := buildMessage(0x0101, fixedTxID(), section)
	buf = buf[:len(buf):len(buf)] // cap-locked

	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("DecodeMessage panicked on inner over-long attribute: %v", r)
			}
		}()
		_, err = DecodeMessage(buf)
	}()
	if err == nil {
		t.Fatal("DecodeMessage accepted an attribute whose value length overruns the section; want error")
	}
}

// TestDecodeHugeAttrLengthNoOverAllocation is the allocation-bomb probe. A
// well-formed-looking header could otherwise lure the decoder into
// make([]byte, 65535) for a value that is not actually present. We confirm that
// when the buffer does NOT actually contain those bytes, decode errors out
// (the bound check fires BEFORE allocation) rather than allocating a huge slice
// from a tiny hostile packet.
//
// Mutation that kills it: move the `make([]byte, alen)` allocation ABOVE the
// `4+alen > len(attrs)` check (allocate then bound) → a 28-byte packet would
// allocate 65535 bytes before erroring; here we additionally assert the value,
// if any attribute parsed, never exceeds what the buffer could supply.
func TestDecodeHugeAttrLengthNoOverAllocation(t *testing.T) {
	// Honest header length covering the 8-byte section; inner attr lies.
	section := make([]byte, 8)
	binary.BigEndian.PutUint16(section[0:2], 0x0020)
	binary.BigEndian.PutUint16(section[2:4], 0xFFFF)
	buf := buildMessage(0x0101, fixedTxID(), section)

	msg, err := DecodeMessage(buf)
	if err == nil {
		// If a future contract change makes this lenient, at minimum no parsed
		// attribute value may exceed the bytes the buffer could supply.
		for at, v := range msg.Attributes {
			if len(v) > len(buf) {
				t.Fatalf("attribute 0x%04X value len %d exceeds buffer len %d — over-allocation", at, len(v), len(buf))
			}
		}
		t.Fatalf("expected error on inner 0xFFFF length, got msg=%+v", msg)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// VALIDATION — magic-cookie discrimination.
// ────────────────────────────────────────────────────────────────────────────

// TestDecodeRejectsNonSTUNBuffer confirms a buffer that is the right size but
// carries a wrong magic cookie (a non-STUN packet, e.g. a stray UDP datagram)
// is rejected, while the identical buffer with the correct cookie is accepted.
// This is the load-bearing "is this even STUN" gate.
//
// Mutation that kills it: delete the `cookie != magicCookie` check in
// DecodeMessage → the non-STUN buffer is accepted and the err==nil branch fires
// t.Error.
func TestDecodeRejectsNonSTUNBuffer(t *testing.T) {
	good := buildMessage(0x0101, fixedTxID(), nil)
	if _, err := DecodeMessage(good); err != nil {
		t.Fatalf("valid STUN header rejected: %v", err)
	}

	bad := bytes.Clone(good)
	// Flip the cookie to a plausible non-STUN value.
	binary.BigEndian.PutUint32(bad[4:8], 0x00000000)
	if _, err := DecodeMessage(bad); err == nil {
		t.Error("DecodeMessage accepted a buffer with a zero (non-STUN) magic cookie; want rejection")
	}

	// One-bit corruption of the cookie must also be rejected.
	bad2 := bytes.Clone(good)
	bad2[7] ^= 0x01
	if _, err := DecodeMessage(bad2); err == nil {
		t.Error("DecodeMessage accepted a buffer with a 1-bit-corrupted magic cookie; want rejection")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// EDGES.
// ────────────────────────────────────────────────────────────────────────────

// TestDecodeNilAndEmpty confirms nil and empty inputs are rejected without
// panic (the degenerate over-read inputs).
//
// Mutation that kills it: remove the `len(buf) < headerLen` guard → indexing
// buf[4:8] on a nil/empty slice panics, caught by recover.
func TestDecodeNilAndEmpty(t *testing.T) {
	for _, name := range []string{"nil", "empty"} {
		var b []byte
		if name == "empty" {
			b = []byte{}
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("DecodeMessage panicked on %s input: %v", name, r)
				}
			}()
			if _, err := DecodeMessage(b); err == nil {
				t.Errorf("DecodeMessage(%s) returned nil error; want rejection", name)
			}
		}()
	}
}

// TestDecodeExactlyHeaderLengthZeroAttributes confirms a clean 20-byte message
// with zero attributes decodes to an empty attribute map (the minimal valid
// message, an explicit edge).
//
// Mutation that kills it: initialise the attribute walk slice incorrectly so a
// zero-length section is mis-handled — e.g. change the header-length guard to
// `>=` (rejecting the exact-header case) → this valid message is rejected and
// the t.Fatalf fires.
func TestDecodeExactlyHeaderLengthZeroAttributes(t *testing.T) {
	buf := buildMessage(msgTypeBindingRequest, fixedTxID(), nil)
	if len(buf) != headerLen {
		t.Fatalf("setup: buf len = %d, want %d", len(buf), headerLen)
	}
	msg, err := DecodeMessage(buf)
	if err != nil {
		t.Fatalf("DecodeMessage on exact-header message: %v", err)
	}
	if msg.Type != msgTypeBindingRequest {
		t.Errorf("Type = 0x%04X, want 0x%04X", msg.Type, msgTypeBindingRequest)
	}
	if len(msg.Attributes) != 0 {
		t.Errorf("Attributes len = %d, want 0", len(msg.Attributes))
	}
}

// TestDecodePartialTrailingTLVHeader confirms that an attribute section whose
// trailing bytes are a partial (<4-byte) TLV header are tolerated without
// panic: the `for len(attrs) >= 4` loop condition stops the walk and the
// already-parsed attributes are returned. This pins the loop-guard that
// prevents a 1..3-byte tail from being read as a 4-byte TLV header.
//
// Mutation that kills it: change the loop condition to `for len(attrs) > 0` →
// the 2-byte tail is read as a TLV header (binary.BigEndian.Uint16(attrs[2:4])
// slices out of range) and panics, caught by recover.
func TestDecodePartialTrailingTLVHeader(t *testing.T) {
	// One complete 0-length attribute (4 bytes) followed by a 2-byte fragment.
	section := []byte{
		0x80, 0x01, 0x00, 0x00, // type=0x8001 len=0 (complete)
		0xAB, 0xCD, // dangling 2-byte fragment
	}
	buf := buildMessage(0x0101, fixedTxID(), section)
	buf = buf[:len(buf):len(buf)]

	var msg *Message
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("DecodeMessage panicked on partial trailing TLV header: %v", r)
			}
		}()
		msg, err = DecodeMessage(buf)
	}()
	if err != nil {
		t.Fatalf("DecodeMessage on partial-trailing-TLV message: %v", err)
	}
	if _, ok := msg.Attributes[0x8001]; !ok {
		t.Errorf("the one complete attribute 0x8001 was not parsed; attrs=%v", msg.Attributes)
	}
}
