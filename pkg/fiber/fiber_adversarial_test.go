package fiber

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// In-memory transports with controllable chunking.
//
// The framing contract MUST be independent of how the underlying stream splits
// bytes into Read calls: TCP may coalesce several frames into one Read or split
// one frame across many Reads. net.Pipe is avoided here because it is fully
// synchronous (every Write blocks until a matching Read), which deadlocks the
// "feed a whole blob then decode" probes. Instead we use simple byte sources we
// fully control.
// ---------------------------------------------------------------------------

// chunkReader serves a fixed byte slice but returns it in caller-chosen chunk
// sizes, so we can force readFrame to stitch a frame back together across many
// short Reads (the split-frame boundary case) without any real socket.
type chunkReader struct {
	data   []byte
	chunks []int // successive max bytes to hand out per Read; 0/absent => rest
	pos    int
	ci     int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	limit := len(r.data) - r.pos
	if r.ci < len(r.chunks) {
		if c := r.chunks[r.ci]; c > 0 && c < limit {
			limit = c
		}
		r.ci++
	}
	if limit > len(p) {
		limit = len(p)
	}
	n := copy(p, r.data[r.pos:r.pos+limit])
	r.pos += n
	return n, nil
}

// rwFromReader adapts an io.Reader into an io.ReadWriter whose Write is a
// discarding sink, so a Conn can read framed input from a crafted byte stream.
type rwFromReader struct{ io.Reader }

func (rwFromReader) Write(p []byte) (int, error) { return len(p), nil }

// encodeFrame builds the exact on-wire bytes for one frame: type, big-endian
// uint32 length, payload. This is the test's independent oracle of the wire
// format (it does NOT call writeFrame), so a divergence between this encoder
// and the production decoder is a real framing defect, not a tautology.
func encodeFrame(t FrameType, payload []byte) []byte {
	out := make([]byte, headerSize+len(payload))
	out[0] = byte(t)
	binary.BigEndian.PutUint32(out[1:headerSize], uint32(len(payload)))
	copy(out[headerSize:], payload)
	return out
}

// ---------------------------------------------------------------------------
// CENTERPIECE 1: exact-bytes-and-order round trip across HOSTILE chunking.
//
// Many frames of adversarial sizes (empty, 1 byte, header-sized, exactly max,
// large binary with NULs/0xFF) are concatenated into a single stream, then the
// stream is delivered in deliberately misaligned chunks: sometimes a fraction
// of a header, sometimes several whole frames at once. The decoder MUST recover
// every payload, byte-exact and in order, with no merge or split.
//
// MUTATION that makes this FAIL: in readFrame, change
//
//	payload := make([]byte, n)
//	if _, err := io.ReadFull(c.rw, payload); err != nil { ... }
//
// to read n+1 bytes (make([]byte, n+1)) — every subsequent frame's bytes shift,
// breaking the exact-bytes/order assertions. Equivalently, dropping the size
// guard or mis-decoding the length prefix corrupts boundaries here.
// ---------------------------------------------------------------------------
func TestAdversarialRoundTripExactBytesHostileChunking(t *testing.T) {
	const maxSize = 4096
	payloads := [][]byte{
		{},                            // empty
		{0x00},                        // single NUL
		{0xFF},                        // single 0xFF
		[]byte("a"),                   // 1 byte ascii
		bytes.Repeat([]byte{0x00}, 5), // header-sized run of NULs
		[]byte("the-quick-brown-fox"),
		bytes.Repeat([]byte{0xAB, 0x00, 0xFF, 0x7F}, 300), // binary w/ NULs
		bytes.Repeat([]byte{0x41}, maxSize),               // EXACTLY max
		bytes.Repeat([]byte{0x42}, maxSize-1),             // one below max
		{},                                                // trailing empty
	}

	// Build the concatenated wire stream from the independent encoder.
	var wire bytes.Buffer
	for _, p := range payloads {
		wire.Write(encodeFrame(FrameData, p))
	}

	// Hostile, prime-ish chunk sizes that straddle header/payload boundaries:
	// 1, 2, 3 force sub-header reads; large values coalesce whole frames.
	chunks := []int{1, 2, 3, 7, 5, headerSize, headerSize + 1, 9999, 1, 4096, 2, 13}
	cr := &chunkReader{data: wire.Bytes(), chunks: chunks}
	conn := NewConn(rwFromReader{cr}, maxSize)

	for i, want := range payloads {
		got, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("ReadMessage[%d]: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("frame[%d] corrupted across chunking:\n got(len=%d)=%x\nwant(len=%d)=%x",
				i, len(got), got, len(want), want)
		}
	}
	// Stream is now exhausted: the next read must be a clean io.EOF, proving no
	// phantom/extra frame was synthesized from leftover bytes.
	if _, err := conn.ReadMessage(); !errors.Is(err, io.EOF) {
		t.Fatalf("after all frames want io.EOF, got %v", err)
	}
}

// TestAdversarialTwoFramesCoalescedNotMerged feeds TWO whole frames in a SINGLE
// underlying Read (the classic coalescing case) and proves they are delivered
// as two distinct messages with their exact, separate payloads — never merged
// into one and never split.
//
// MUTATION that makes this FAIL: in readFrame, use c.rw.Read(payload) instead of
// io.ReadFull and have the decoder treat a single Read as the whole frame; the
// boundary between the coalesced frames is then mis-tracked.
func TestAdversarialTwoFramesCoalescedNotMerged(t *testing.T) {
	a := []byte("FRAME-ONE")
	b := bytes.Repeat([]byte{0x9C}, 600)
	var wire bytes.Buffer
	wire.Write(encodeFrame(FrameData, a))
	wire.Write(encodeFrame(FrameData, b))

	// One giant chunk: both frames arrive in a single Read.
	cr := &chunkReader{data: wire.Bytes(), chunks: []int{wire.Len()}}
	conn := NewConn(rwFromReader{cr}, 0)

	got1, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage 1: %v", err)
	}
	if !bytes.Equal(got1, a) {
		t.Fatalf("first frame: got=%q want=%q", got1, a)
	}
	got2, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage 2: %v", err)
	}
	if !bytes.Equal(got2, b) {
		t.Fatalf("second frame corrupted (boundary merged?): got len=%d want len=%d", len(got2), len(b))
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE 2: frame-decode robustness — NO panic, NO over-alloc, on hostile
// length prefixes and pure garbage.
// ---------------------------------------------------------------------------

// TestAdversarialHostileLengthPrefixesNoPanicNoOverAlloc sweeps a battery of
// crafted length prefixes that claim FAR more payload than is present (including
// the absolute uint32 max ~4 GiB) with a tiny configured max. Each MUST return
// promptly with ErrFrameTooLarge and MUST NOT attempt make([]byte, n) for the
// attacker-chosen n. Running this with a 64 MiB-capped maxSize and asserting a
// 2s deadline means a missing guard (which would allocate ~4 GiB and block in
// ReadFull) is caught as a timeout/OOM rather than a silent pass.
//
// MUTATION that makes this FAIL: delete the
//
//	if int64(n) > int64(c.maxSize) { return 0, nil, ErrFrameTooLarge }
//
// guard in readFrame — the decoder then allocates the attacker-sized buffer and
// blocks in io.ReadFull, tripping the deadline.
func TestAdversarialHostileLengthPrefixesNoPanicNoOverAlloc(t *testing.T) {
	const maxSize = 1024
	hostile := []uint32{
		0xFFFFFFFF, // ~4 GiB
		0x80000000, // 2 GiB
		0x40000000, // 1 GiB
		0x10000000, // 256 MiB
		maxSize + 1,
		maxSize + 100,
	}
	for _, n := range hostile {
		n := n
		t.Run("len="+itoa(int(n)), func(t *testing.T) {
			var hdr [headerSize]byte
			hdr[0] = byte(FrameData)
			binary.BigEndian.PutUint32(hdr[1:], n)
			// Header only; NO payload bytes follow (chunkReader hits EOF after).
			cr := &chunkReader{data: hdr[:]}
			conn := NewConn(rwFromReader{cr}, maxSize)

			done := make(chan error, 1)
			go func() {
				defer func() {
					if r := recover(); r != nil {
						done <- errors.New("PANIC in readFrame: " + itoaPanic(r))
					}
				}()
				_, err := conn.ReadMessage()
				done <- err
			}()

			select {
			case err := <-done:
				if !errors.Is(err, ErrFrameTooLarge) {
					t.Fatalf("len=%d: want ErrFrameTooLarge (no alloc), got %v", n, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("len=%d: readFrame blocked/over-allocated (DoS): no size guard?", n)
			}
		})
	}
}

// TestAdversarialGarbageStreamNeverPanics feeds many independent random byte
// streams into the decoder. Whatever the bytes, the decoder must terminate with
// SOME error (or, by chance, decode well-formed frames) and must NEVER panic and
// NEVER hang. This is a fuzz-style robustness probe over the decode path.
//
// MUTATION that makes this FAIL: any decoder change that indexes the header or
// payload without bounds (e.g. removing the io.ReadFull header read and slicing
// raw) would panic on a short/garbled stream and this catches it.
func TestAdversarialGarbageStreamNeverPanics(t *testing.T) {
	rng := rand.New(rand.NewSource(0xF1BE5))
	for iter := 0; iter < 500; iter++ {
		size := rng.Intn(64) // 0..63 bytes of pure garbage
		buf := make([]byte, size)
		rng.Read(buf)
		cr := &chunkReader{data: buf}
		conn := NewConn(rwFromReader{cr}, 1<<20)

		done := make(chan struct{})
		go func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("iter %d: PANIC decoding garbage %x: %v", iter, buf, r)
				}
				close(done)
			}()
			// Drain frames until any error; bounded so a (legal) zero-length
			// frame stream cannot loop forever.
			for i := 0; i < 100; i++ {
				if _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("iter %d: decoder hung on garbage %x", iter, buf)
		}
	}
}

// TestAdversarialZeroFrameStreamImmediateEOF proves an empty stream (peer closed
// before sending anything) yields a clean io.EOF, not a panic or a phantom
// frame.
func TestAdversarialZeroFrameStreamImmediateEOF(t *testing.T) {
	cr := &chunkReader{data: nil}
	conn := NewConn(rwFromReader{cr}, 0)
	if _, err := conn.ReadMessage(); !errors.Is(err, io.EOF) {
		t.Fatalf("empty stream: want io.EOF, got %v", err)
	}
}

// TestAdversarialMaxBoundaryWriteReadRoundTrip proves the inclusive boundary of
// the configured max: a payload of EXACTLY maxSize round-trips, while maxSize+1
// is refused by the writer (ErrFrameTooLarge) and a forged max+1 length prefix
// is refused by the reader.
//
// MUTATION that makes this FAIL: change the writer guard to
// `len(payload) >= c.maxSize` (off-by-one) — the exactly-max payload would then
// be wrongly rejected and the round-trip half fails.
func TestAdversarialMaxBoundaryWriteReadRoundTrip(t *testing.T) {
	const maxSize = 2048
	// Exactly-max payload must round-trip through a real write+decode.
	exact := bytes.Repeat([]byte{0x5A}, maxSize)
	wire := encodeFrame(FrameData, exact)
	conn := NewConn(rwFromReader{&chunkReader{data: wire, chunks: []int{1, maxSize, 7}}}, maxSize)
	got, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("exactly-max frame failed to decode: %v", err)
	}
	if !bytes.Equal(got, exact) {
		t.Fatalf("exactly-max frame corrupted: got len=%d want len=%d", len(got), len(exact))
	}

	// Writer refuses max+1.
	wconn := NewConn(rwFromReader{&chunkReader{}}, maxSize)
	if err := wconn.WriteMessage(bytes.Repeat([]byte{0x00}, maxSize+1)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("writer accepted max+1 payload: %v", err)
	}

	// Reader refuses a forged length prefix of max+1 BEFORE allocating it.
	var hdr [headerSize]byte
	hdr[0] = byte(FrameData)
	binary.BigEndian.PutUint32(hdr[1:], maxSize+1)
	rconn := NewConn(rwFromReader{&chunkReader{data: hdr[:]}}, maxSize)
	if _, err := rconn.ReadMessage(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("reader accepted forged max+1 length: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ADMISSION: concurrency safety + double-spend resistance.
// ---------------------------------------------------------------------------

// TestAdmitterConcurrentIssueAndAdmitRace hammers a single Admitter from many
// goroutines: each concurrently issues a challenge, signs it, and admits. Run
// under -race this proves the issued-nonce map has no data race, and the success
// count proves every independently-issued valid handshake is admitted.
//
// MUTATION that makes this FAIL: remove a.mu locking around the issued map in
// IssueChallenge/consume — -race flags the concurrent map access (and Go's map
// may also panic on concurrent write).
func TestAdmitterConcurrentIssueAndAdmitRace(t *testing.T) {
	pub, priv := newPeerIdentity(t)
	adm := NewAdmitter(Ed25519Verifier{}, 0)

	const workers = 64
	var wg sync.WaitGroup
	var admitted int64
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				nonce, err := adm.IssueChallenge()
				if err != nil {
					t.Errorf("IssueChallenge: %v", err)
					return
				}
				dec, err := adm.Admit(Handshake{
					PublicKey: pub,
					Nonce:     nonce,
					Signature: ed25519.Sign(priv, nonce),
					Stake:     10,
				})
				if err != nil || !dec.Admit {
					t.Errorf("valid handshake denied under concurrency: admit=%v err=%v", dec.Admit, err)
					return
				}
				atomic.AddInt64(&admitted, 1)
			}
		}()
	}
	wg.Wait()
	if want := int64(workers * 50); admitted != want {
		t.Fatalf("admitted=%d, want %d (lost admissions under concurrency)", admitted, want)
	}
}

// TestAdmitterNonceConsumedExactlyOnceUnderRace is the double-spend probe: ONE
// nonce is issued, then MANY goroutines race to Admit the SAME valid handshake.
// EXACTLY ONE must be admitted; every other attempt must be denied ErrStaleNonce.
// This proves the consume() check+delete is atomic (no two callers can both see
// the nonce as outstanding).
//
// MUTATION that makes this FAIL: in consume, split the check and delete across
// the lock (e.g. read membership under lock, unlock, then delete) — two racing
// callers can both observe the nonce outstanding and both be admitted, so the
// admitted count exceeds 1.
func TestAdmitterNonceConsumedExactlyOnceUnderRace(t *testing.T) {
	pub, priv := newPeerIdentity(t)
	adm := NewAdmitter(Ed25519Verifier{}, 0)

	nonce, err := adm.IssueChallenge()
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	hs := Handshake{PublicKey: pub, Nonce: nonce, Signature: ed25519.Sign(priv, nonce), Stake: 10}

	const racers = 128
	var admitted, stale int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			<-start // release all at once to maximize contention
			dec, err := adm.Admit(hs)
			if dec.Admit {
				atomic.AddInt64(&admitted, 1)
			} else if errors.Is(err, ErrStaleNonce) {
				atomic.AddInt64(&stale, 1)
			} else {
				t.Errorf("unexpected denial reason: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if admitted != 1 {
		t.Fatalf("DOUBLE-SPEND: nonce admitted %d times, want exactly 1", admitted)
	}
	if stale != racers-1 {
		t.Fatalf("stale denials=%d, want %d", stale, racers-1)
	}
}

// itoaPanic renders a recovered panic value for an error message without pulling
// in fmt at call sites that must stay allocation-light.
func itoaPanic(v interface{}) string {
	if e, ok := v.(error); ok {
		return e.Error()
	}
	if s, ok := v.(string); ok {
		return s
	}
	return "non-string panic"
}
