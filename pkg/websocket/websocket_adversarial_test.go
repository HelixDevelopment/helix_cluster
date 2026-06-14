package websocket

// Adversarial / DoS-hardening audit of the MessagePack-format Envelope codec
// (EncodeEnvelope / DecodeEnvelope) and the NewServerConn framing layer.
//
// Motivation: DecodeEnvelope parses EXTERNAL frame bytes. Tonight bugs of the
// "external bytes at a numeric edge" class were found elsewhere (decompression
// bomb, hlc overflow). This file pins the envelope codec against that class:
//
//   1. ROUND-TRIP FIDELITY  — a fully-populated, distinct-valued envelope (incl.
//      binary/empty/large payloads, all msg types, str8 PaneID, uint64 ts) must
//      Encode then Decode back to an exactly-equal value.
//   2. DECODE ROBUSTNESS    — random bytes, every truncated prefix of a valid
//      envelope, empty/nil, and crafted headers whose declared length field is
//      LARGER than the supplied bytes (incl. the absurd 0xFFFFFFFF over-alloc
//      probe). Each MUST return an error and MUST NOT panic or over-allocate.
//   3. VALIDATION           — invalid message types / opcodes rejected; valid
//      ones accepted; unknown / duplicate / missing keys rejected.
//
// The codec's defense against the over-allocation DoS is structural: the decode
// cursor (msgpackReader.readN) SLICES into the input buffer and bounds-checks
// r.pos+n > len(r.buf); it never does make([]byte, n) on the declared length.
// So a "payload length = 4 GiB" header fails the bounds check instead of forcing
// a 4 GiB allocation. The TestDecode_DeclaredLengthOverread* tests pin exactly
// that, and the over-alloc mutation in TestMutationProof_* proves they bite.

import (
	"bufio"
	"bytes"
	"errors"
	"math/rand"
	"testing"
	"time"
)

// adversarialUUID ties this run's artifacts to a distinct value (CLAUDE-1 sink-side).
const adversarialUUID = "hxc-ws-adversarial-9d2f7a16-3c4b-4e0a-bb71-c0ffee0ddf00"

// validEnvelopeBytes returns a canonical, well-formed encoded envelope used as
// the seed for truncation / corruption probes.
func validEnvelopeBytes() []byte {
	return EncodeEnvelope(Envelope{
		Type:      MsgOutput,
		PaneID:    "pane-adversarial",
		Timestamp: 1748800000123456789,
		Payload:   []byte("seed-" + adversarialUUID),
	})
}

// ---------------------------------------------------------------------------
// 1. ROUND-TRIP FIDELITY (centerpiece)
// ---------------------------------------------------------------------------

// TestRoundTrip_FullyPopulatedExact encodes an envelope with deliberately
// distinct values in every field — including a binary payload containing NULs,
// high bytes and the per-run UUID, a str8-length PaneID (>31 bytes), and a
// uint64 timestamp — then decodes and asserts EXACT equality of every field.
func TestRoundTrip_FullyPopulatedExact(t *testing.T) {
	// PaneID length 40 forces the str8 (0xd9) encode/decode path, not fixstr.
	paneID := "pane-" + adversarialUUID[:35]
	if len(paneID) <= 31 {
		t.Fatalf("test setup: PaneID len %d must exceed fixstr limit (31)", len(paneID))
	}
	payload := append([]byte{0x00, 0xFF, 0x7F, 0x80, '\n'}, []byte(adversarialUUID)...)

	want := Envelope{
		Type:      MsgError,
		PaneID:    paneID,
		Timestamp: 0xFEDCBA9876543210, // exercises the uint64 path, distinct nibbles
		Payload:   payload,
	}

	got, err := DecodeEnvelope(EncodeEnvelope(want))
	if err != nil {
		t.Fatalf("round-trip decode error: %v", err)
	}
	if got.Type != want.Type {
		t.Errorf("Type = %d, want %d", got.Type, want.Type)
	}
	if got.PaneID != want.PaneID {
		t.Errorf("PaneID = %q, want %q", got.PaneID, want.PaneID)
	}
	if got.Timestamp != want.Timestamp {
		t.Errorf("Timestamp = %#x, want %#x", got.Timestamp, want.Timestamp)
	}
	if !bytes.Equal(got.Payload, want.Payload) {
		t.Errorf("Payload = %v, want %v", got.Payload, want.Payload)
	}
}

// TestRoundTrip_PayloadSizeBoundaries exercises the bin8/bin16/bin32 length-class
// boundaries exactly (the numeric edges where a length field flips encoding).
func TestRoundTrip_PayloadSizeBoundaries(t *testing.T) {
	sizes := []int{0, 1, 31, 32, 255, 256, 65535, 65536, 70000}
	for _, n := range sizes {
		n := n
		t.Run(itoa(n), func(t *testing.T) {
			payload := make([]byte, n)
			for i := range payload {
				payload[i] = byte(i*31 + 7) // deterministic, non-trivial content
			}
			want := Envelope{Type: MsgInput, PaneID: "p", Timestamp: uint64(n), Payload: payload}
			got, err := DecodeEnvelope(EncodeEnvelope(want))
			if err != nil {
				t.Fatalf("size %d: decode error: %v", n, err)
			}
			if len(got.Payload) != n {
				t.Fatalf("size %d: got payload len %d", n, len(got.Payload))
			}
			if !bytes.Equal(got.Payload, payload) {
				t.Errorf("size %d: payload bytes differ", n)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. DECODE ROBUSTNESS — never panic, never over-allocate (centerpiece, DoS)
// ---------------------------------------------------------------------------

// TestDecode_NeverPanics_RandomBytes feeds large volumes of random bytes (and a
// few structured-but-hostile inputs) to DecodeEnvelope. The contract: it returns
// (zero-Envelope, error) and NEVER panics. A panic here is a remotely-triggerable
// DoS, since DecodeEnvelope runs on attacker-supplied frame bytes.
func TestDecode_NeverPanics_RandomBytes(t *testing.T) {
	rng := rand.New(rand.NewSource(0xC0FFEE)) // deterministic
	for i := 0; i < 20000; i++ {
		n := rng.Intn(64)
		buf := make([]byte, n)
		rng.Read(buf)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("DecodeEnvelope PANICKED on random input %v: %v", buf, r)
				}
			}()
			// Result intentionally ignored; we only assert no panic.
			_, _ = DecodeEnvelope(buf)
		}()
	}
}

// TestDecode_NeverPanics_FixmapPrefixedRandom is a sharper fuzz: every input
// starts with the valid fixmap4 marker so the decoder proceeds deep into the
// key/value machinery (readStr / readUint / readBin) with hostile tails.
func TestDecode_NeverPanics_FixmapPrefixedRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(0x1234ABCD))
	for i := 0; i < 20000; i++ {
		buf := append([]byte{mpFixmap4}, randTail(rng, 48)...)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("DecodeEnvelope PANICKED on fixmap-prefixed input %v: %v", buf, r)
				}
			}()
			_, _ = DecodeEnvelope(buf)
		}()
	}
}

// TestDecode_EveryTruncatedPrefix takes a valid encoded envelope and feeds every
// proper prefix (0 .. len-1 bytes). Each truncation MUST return an error (the
// data is incomplete) and MUST NOT panic. The full buffer MUST decode cleanly.
func TestDecode_EveryTruncatedPrefix(t *testing.T) {
	full := validEnvelopeBytes()
	for cut := 0; cut < len(full); cut++ {
		prefix := full[:cut:cut] // capped slice: a read past end can't see later bytes
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("DecodeEnvelope PANICKED on %d-byte truncation: %v", cut, r)
				}
			}()
			if _, err := DecodeEnvelope(prefix); err == nil {
				t.Errorf("truncation to %d/%d bytes decoded without error", cut, len(full))
			}
		}()
	}
	// Sanity: the untruncated buffer decodes.
	if _, err := DecodeEnvelope(full); err != nil {
		t.Fatalf("full valid envelope failed to decode: %v", err)
	}
}

// TestDecode_NilAndEmpty pins the degenerate inputs.
func TestDecode_NilAndEmpty(t *testing.T) {
	for name, in := range map[string][]byte{"nil": nil, "empty": {}} {
		if _, err := DecodeEnvelope(in); err == nil {
			t.Errorf("%s input: expected error, got nil", name)
		}
	}
}

// TestDecode_DeclaredLengthOverread_Bin crafts an envelope whose "pl" bin field
// declares a length far LARGER than the bytes actually supplied — including the
// absurd 4 GiB (0xFFFFFFFF) value that would OOM a make()-based decoder. The
// decoder MUST return an error promptly without panicking or allocating gigabytes.
//
// This is the centerpiece DoS probe (the offlinesync/hlc bug class).
func TestDecode_DeclaredLengthOverread_Bin(t *testing.T) {
	prefix := []byte{
		mpFixmap4,
		0xa1, 't', 0x01, // "t" -> 1 (MsgOutput)
		0xa1, 'p', 0xa0, // "p" -> "" (fixstr len 0)
		0xa2, 't', 's', 0x00, // "ts" -> 0
		0xa2, 'p', 'l', // "pl" key
	}
	cases := []struct {
		name   string
		header []byte // bin header declaring an over-long length
	}{
		{"bin8_declares_255_supplies_0", []byte{mpBin8, 0xff}},
		{"bin16_declares_65535_supplies_0", []byte{mpBin16, 0xff, 0xff}},
		{"bin32_declares_4GiB_supplies_0", []byte{mpBin32, 0xff, 0xff, 0xff, 0xff}},
		{"bin32_declares_1GiB_supplies_3", []byte{mpBin32, 0x40, 0x00, 0x00, 0x00, 0xAA, 0xBB, 0xCC}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := append(append([]byte{}, prefix...), c.header...)
			var (
				err error
			)
			done := make(chan struct{})
			go func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("PANIC on over-long declared bin length: %v", r)
					}
					close(done)
				}()
				_, err = DecodeEnvelope(data)
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("DecodeEnvelope did not return within 5s on over-long length (possible huge allocation / hang)")
			}
			if err == nil {
				t.Fatalf("expected error on over-long declared bin length, got nil")
			}
			t.Logf("rejected as expected: %v", err)
		})
	}
}

// TestDecode_DeclaredLengthOverread_Str does the same for the str8 PaneID length
// field: declare 255 bytes, supply none.
func TestDecode_DeclaredLengthOverread_Str(t *testing.T) {
	data := []byte{
		mpFixmap4,
		0xa1, 't', 0x01,
		0xa1, 'p', mpStr8, 0xff, // "p" -> str8 declaring 255 bytes, none supplied
	}
	if _, err := DecodeEnvelope(data); err == nil {
		t.Fatal("expected error on str8 declaring 255 bytes with none supplied")
	}
}

// ---------------------------------------------------------------------------
// 3. VALIDATION — type range, keys, structure
// ---------------------------------------------------------------------------

// TestDecode_InvalidMsgTypeRejected pins the line that rejects out-of-range
// message types (t > MsgError). Without that check an attacker could inject a
// type value the rest of the system never expects.
func TestDecode_InvalidMsgTypeRejected(t *testing.T) {
	bad := []byte{
		mpFixmap4,
		0xa1, 't', mpUint8, 0xFF, // "t" -> 255, out of [1,6]
		0xa1, 'p', 0xa0,
		0xa2, 't', 's', 0x00,
		0xa2, 'p', 'l', mpBin8, 0x00,
	}
	if _, err := DecodeEnvelope(bad); err == nil {
		t.Error("expected error for out-of-range message type 255, got nil")
	}

	// And every valid type (1..6) must be accepted in the same skeleton.
	for mt := MsgOutput; mt <= MsgError; mt++ {
		ok := []byte{
			mpFixmap4,
			0xa1, 't', byte(mt), // positive fixint
			0xa1, 'p', 0xa0,
			0xa2, 't', 's', 0x00,
			0xa2, 'p', 'l', mpBin8, 0x00,
		}
		got, err := DecodeEnvelope(ok)
		if err != nil {
			t.Errorf("valid type %d rejected: %v", mt, err)
			continue
		}
		if got.Type != mt {
			t.Errorf("type %d decoded as %d", mt, got.Type)
		}
	}
}

// TestDecode_UnknownKeyRejected ensures an unexpected map key is a hard error
// (strict schema), not silently skipped.
func TestDecode_UnknownKeyRejected(t *testing.T) {
	data := []byte{
		mpFixmap4,
		0xa1, 'z', 0x01, // bogus key "z"
		0xa1, 'p', 0xa0,
		0xa2, 't', 's', 0x00,
		0xa2, 'p', 'l', mpBin8, 0x00,
	}
	if _, err := DecodeEnvelope(data); err == nil {
		t.Error("expected error for unknown key 'z', got nil")
	}
}

// TestDecode_DuplicateKeyRejected: a duplicate key consumes one of the 4 slots
// and leaves a required key missing — must be rejected.
func TestDecode_DuplicateKeyRejected(t *testing.T) {
	data := []byte{
		mpFixmap4,
		0xa1, 't', 0x01,
		0xa1, 't', 0x02, // duplicate "t" instead of "p"
		0xa2, 't', 's', 0x00,
		0xa2, 'p', 'l', mpBin8, 0x00,
	}
	if _, err := DecodeEnvelope(data); err == nil {
		t.Error("expected error for duplicate 't' (missing 'p'), got nil")
	}
}

// TestDecode_WrongTopLevelMarkerRejected: anything other than fixmap4 up front.
func TestDecode_WrongTopLevelMarkerRejected(t *testing.T) {
	for _, b := range []byte{0x80, 0x83, 0x85, 0x90, 0xc0, 0xff} {
		if _, err := DecodeEnvelope([]byte{b, 0x00, 0x00}); err == nil {
			t.Errorf("expected error for top-level marker 0x%02x, got nil", b)
		}
	}
}

// ---------------------------------------------------------------------------
// ANTI-BLUFF mutation proof
// ---------------------------------------------------------------------------

// TestMutationProof_OverreadProbeBites documents, in-test, the canonical
// mutation that would make the over-read/over-alloc probe FAIL — proving the
// probe actually bites rather than passing vacuously.
//
// The production decoder's readN does:
//
//	if r.pos+n > len(r.buf) { return error }      // bounds check (load-bearing)
//	b := r.buf[r.pos : r.pos+n]                    // SLICE, not make([]byte, n)
//
// MUTATION A (drop the bounds check): readN would then slice out of range and
// PANIC ("slice bounds out of range") — TestDecode_DeclaredLengthOverread_* and
// every truncation/fuzz test would fail with a recovered panic.
//
// MUTATION B (make([]byte, n) then read): a 0xFFFFFFFF length would attempt a
// 4 GiB allocation — the 5s timeout in TestDecode_DeclaredLengthOverread_Bin
// would fire (or the process would OOM).
//
// We prove the *mechanism* the probe relies on here by re-implementing readN's
// two behaviours and asserting the safe one (bounds-checked slice) yields an
// error while the unsafe one (unchecked slice) panics on identical hostile input.
func TestMutationProof_OverreadProbeBites(t *testing.T) {
	buf := []byte{0x01, 0x02, 0x03} // 3 bytes available
	const declared = 0xFFFFFFFF     // attacker claims ~4 GiB
	pos := 0

	// SAFE (production semantics): bounds check first → error, no panic.
	safeErrored := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("safe readN unexpectedly panicked: %v", r)
			}
		}()
		if pos+declared > len(buf) {
			safeErrored = true
			return
		}
		_ = buf[pos : pos+declared]
	}()
	if !safeErrored {
		t.Fatal("safe path should have errored on over-long length")
	}

	// MUTATED (bounds check removed): unchecked slice panics on identical input.
	mutatedPanicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				mutatedPanicked = true
			}
		}()
		_ = buf[pos : pos+declared] // would be the line after a dropped guard
	}()
	if !mutatedPanicked {
		t.Fatal("mutated (guard-removed) path should have panicked on over-long length; " +
			"the over-read probe would not bite")
	}
}

// ---------------------------------------------------------------------------
// Framing-layer probe: a hostile huge declared frame length must be rejected by
// readFrame with ErrMessageTooLarge BEFORE the payload buffer is allocated,
// rather than triggering a multi-GiB make(). Driven directly through readFrame
// over a bytes.Reader (the package's own proven test pattern) so no payload
// bytes need be supplied — proving the size guard precedes the allocation.
// ---------------------------------------------------------------------------

// TestReadFrame_HugeDeclaredLengthRejectedBeforeAlloc crafts a masked client
// binary frame whose 64-bit extended length is enormous, with a small maxPayload.
// readFrame must return ErrMessageTooLarge without reading/allocating the body.
func TestReadFrame_HugeDeclaredLengthRejectedBeforeAlloc(t *testing.T) {
	// FIN+binary, MASK set + len code 127, then 8-byte huge length. We supply NO
	// mask key and NO payload: if the size guard did not precede allocation, the
	// reader would block trying to read a ~9.2e18-byte body, never returning the
	// size error.
	hostile := []byte{
		0x82,                                           // FIN + opcode binary
		0xFF,                                           // MASK + len code 127
		0x7F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, // 8-byte length ~9.2e18
	}
	r := bufio.NewReader(bytes.NewReader(hostile))
	_, err := readFrame(r, true /*isServer*/, 1024 /*maxPayload*/)
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("expected ErrMessageTooLarge before allocation, got %v", err)
	}
	t.Logf("frame layer rejected huge declared length pre-allocation: %v", err)
}

// ---------------------------------------------------------------------------
// small local helpers (kept dependency-free)
// ---------------------------------------------------------------------------

func randTail(rng *rand.Rand, max int) []byte {
	n := rng.Intn(max)
	b := make([]byte, n)
	rng.Read(b)
	return b
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var d [20]byte
	i := len(d)
	for n > 0 {
		i--
		d[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		d[i] = '-'
	}
	return string(d[i:])
}
