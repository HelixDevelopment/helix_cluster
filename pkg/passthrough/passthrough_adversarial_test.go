package passthrough

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
)

// This package is NOT an HTTP proxy. It is a disconnect-aware streaming pump:
// it relays Chunk values (Seq + Data []byte) from an injected upstream Producer
// to a client Sink, fires an AbortSignal when the client ctx is cancelled, and
// surfaces the Producer's error. The "fidelity contract" therefore concerns the
// streamed payload, not HTTP headers/status. These adversarial tests pin:
//
//   a. DATA fidelity   — Chunk.Data is relayed byte-exact (binary, empty, nil,
//                        large) with no truncation/mutation/aliasing corruption.
//   b. SEQ fidelity    — Chunk.Seq is relayed verbatim, never overwritten or
//                        re-derived by Run.
//   c. ORDER fidelity  — chunks reach the sink in emit order.
//   d. ERROR surfacing — a Producer error mid-stream is surfaced verbatim via
//                        both res.ProducerErr and the returned error; chunks
//                        emitted before the failure are still delivered, and the
//                        count is honest.
//   e. COUNT fidelity  — res.ChunksDelivered equals the actual number of sink
//                        writes (not inflated, not the producer's emit count).

// collectingSink records every chunk it receives, deep-copying Data so a later
// mutation of the producer-owned backing array cannot retroactively change what
// we assert against (we want to capture exactly what crossed the boundary).
type collectingSink struct {
	mu     sync.Mutex
	chunks []Chunk
}

func (s *collectingSink) Write(c Chunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := Chunk{Seq: c.Seq}
	if c.Data != nil {
		cp.Data = append([]byte(nil), c.Data...)
	}
	s.chunks = append(s.chunks, cp)
	return nil
}

func (s *collectingSink) snapshot() []Chunk {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Chunk, len(s.chunks))
	copy(out, s.chunks)
	return out
}

// --- a. DATA fidelity: binary, empty, nil, large all relayed byte-exact -------

func TestRun_DataFidelity_BinaryEmptyNilLarge(t *testing.T) {
	id := runUUID(t)

	// A deliberately adversarial payload set:
	//   - binary with all byte values incl. 0x00 and 0xFF (no text assumptions)
	//   - explicit empty slice (len 0, non-nil)
	//   - nil slice
	//   - a large (1 MiB) payload to expose any chunking/truncation
	full := make([]byte, 256)
	for i := range full {
		full[i] = byte(i)
	}
	large := make([]byte, 1<<20)
	for i := range large {
		large[i] = byte(i*7 + 13)
	}
	want := [][]byte{
		full,
		{},
		nil,
		{0x00},
		{0xFF, 0x00, 0xFF, 0x00},
		large,
	}

	producer := func(abort *AbortSignal, emit Emit) error {
		for i, d := range want {
			if err := emit(Chunk{Seq: i, Data: d}); err != nil {
				return err
			}
		}
		return nil
	}

	sink := &collectingSink{}
	res, err := Run(context.Background(), producer, sink)
	if err != nil {
		t.Fatalf("run=%s unexpected error: %v", id, err)
	}
	got := sink.snapshot()
	if len(got) != len(want) {
		t.Fatalf("run=%s delivered %d chunks, want %d", id, len(got), len(want))
	}
	if res.ChunksDelivered != len(want) {
		t.Fatalf("run=%s ChunksDelivered=%d want %d", id, res.ChunksDelivered, len(want))
	}
	for i := range want {
		// byte-exact relay, including length. A truncated/mutated body fails here.
		if !bytes.Equal(got[i].Data, want[i]) {
			t.Fatalf("run=%s chunk %d Data mismatch:\n got=%v (len %d)\nwant=%v (len %d)",
				id, i, got[i].Data, len(got[i].Data), want[i], len(want[i]))
		}
		if got[i].Seq != i {
			t.Fatalf("run=%s chunk %d Seq=%d want %d", id, i, got[i].Seq, i)
		}
	}
	t.Logf("run=%s data-fidelity OK: %d chunks, largest=%d bytes", id, len(got), len(large))
}

// --- b/c. SEQ + ORDER fidelity: non-monotonic, gappy, negative Seq relayed ----
// verbatim and in emit order. Run must NOT renumber or reorder.
func TestRun_SeqFidelity_VerbatimAndOrdered(t *testing.T) {
	id := runUUID(t)

	// Adversarial Seq values: out of natural order, gaps, duplicates, negative.
	// The pump must relay them exactly as emitted, in emit order.
	seqs := []int{42, 7, 7, -1, 0, 1000000, 3}

	producer := func(abort *AbortSignal, emit Emit) error {
		for _, s := range seqs {
			if err := emit(Chunk{Seq: s, Data: []byte{byte(s)}}); err != nil {
				return err
			}
		}
		return nil
	}

	sink := &collectingSink{}
	res, err := Run(context.Background(), producer, sink)
	if err != nil {
		t.Fatalf("run=%s unexpected error: %v", id, err)
	}
	got := sink.snapshot()
	if len(got) != len(seqs) {
		t.Fatalf("run=%s delivered %d, want %d", id, len(got), len(seqs))
	}
	for i, s := range seqs {
		if got[i].Seq != s {
			t.Fatalf("run=%s position %d: Seq=%d want %d (Run reordered/renumbered?)",
				id, i, got[i].Seq, s)
		}
		if len(got[i].Data) != 1 || got[i].Data[0] != byte(s) {
			t.Fatalf("run=%s position %d: Data=%v want [%d]", id, i, got[i].Data, byte(s))
		}
	}
	if res.ChunksDelivered != len(seqs) {
		t.Fatalf("run=%s ChunksDelivered=%d want %d", id, res.ChunksDelivered, len(seqs))
	}
	t.Logf("run=%s seq-fidelity OK: relayed %v verbatim in order", id, seqs)
}

// --- d. ERROR surfacing: a Producer failure mid-stream is reported verbatim ---
// via BOTH res.ProducerErr and the returned error, the pre-failure chunks are
// delivered, the count is honest, and the upstream was NOT spuriously aborted.
func TestRun_ProducerErrorSurfacedVerbatim(t *testing.T) {
	id := runUUID(t)
	baseline := runtime.NumGoroutine()

	wantErr := errors.New("upstream boom")
	const okChunks = 4 // emitted successfully before the producer fails

	producer := func(abort *AbortSignal, emit Emit) error {
		for i := 0; i < okChunks; i++ {
			if err := emit(Chunk{Seq: i, Data: []byte{byte(i)}}); err != nil {
				return err
			}
		}
		return wantErr
	}

	sink := &collectingSink{}
	res, err := Run(context.Background(), producer, sink)

	// The upstream error must be surfaced, not swallowed into a success.
	if !errors.Is(err, wantErr) {
		t.Fatalf("run=%s Run returned %v, want %v (error swallowed?)", id, err, wantErr)
	}
	if !errors.Is(res.ProducerErr, wantErr) {
		t.Fatalf("run=%s res.ProducerErr=%v, want %v", id, res.ProducerErr, wantErr)
	}
	// Pre-failure chunks must have been delivered faithfully.
	got := sink.snapshot()
	if len(got) != okChunks {
		t.Fatalf("run=%s delivered %d chunks before failure, want %d", id, len(got), okChunks)
	}
	if res.ChunksDelivered != okChunks {
		t.Fatalf("run=%s ChunksDelivered=%d want %d", id, res.ChunksDelivered, okChunks)
	}
	for i := 0; i < okChunks; i++ {
		if got[i].Seq != i || len(got[i].Data) != 1 || got[i].Data[0] != byte(i) {
			t.Fatalf("run=%s pre-failure chunk %d corrupted: %+v", id, i, got[i])
		}
	}
	// A producer error is NOT a client disconnect: abort must not have fired.
	if res.UpstreamAborted {
		t.Fatalf("run=%s UpstreamAborted=true on producer error (spurious abort)", id)
	}

	got2 := waitGoroutines(t, baseline, 1)
	if got2 > baseline+1 {
		t.Fatalf("run=%s goroutine leak: baseline=%d after=%d", id, baseline, got2)
	}
	t.Logf("run=%s producer-error surfaced verbatim; %d pre-failure chunks intact", id, okChunks)
}

// --- e. Empty stream: a producer that emits nothing and returns nil must yield
// a clean, honest zero-chunk result (no phantom chunk, no spurious error/abort).
func TestRun_EmptyStreamCleanZero(t *testing.T) {
	id := runUUID(t)

	producer := func(abort *AbortSignal, emit Emit) error { return nil }

	sink := &collectingSink{}
	res, err := Run(context.Background(), producer, sink)
	if err != nil {
		t.Fatalf("run=%s expected nil error, got %v", id, err)
	}
	if res.ChunksDelivered != 0 {
		t.Fatalf("run=%s ChunksDelivered=%d want 0", id, res.ChunksDelivered)
	}
	if len(sink.snapshot()) != 0 {
		t.Fatalf("run=%s sink received phantom chunks: %d", id, len(sink.snapshot()))
	}
	if res.UpstreamAborted {
		t.Fatalf("run=%s UpstreamAborted on empty clean stream", id)
	}
	if res.ProducerErr != nil {
		t.Fatalf("run=%s ProducerErr=%v on empty clean stream", id, res.ProducerErr)
	}
	t.Logf("run=%s empty stream clean: 0 chunks, no error, no abort", id)
}

// --- COUNT fidelity centerpiece: ChunksDelivered must equal what actually
// reached the sink, even though the producer attempts to emit MORE than the sink
// accepts before the sink fails. This pins that Run counts SINK-SIDE deliveries,
// not producer-side emits.
func TestRun_ChunksDeliveredCountsSinkSide(t *testing.T) {
	id := runUUID(t)

	wantErr := errors.New("sink stop")
	const failAt = 6 // the 6th sink write fails; 5 succeed

	emitted := 0
	producer := func(abort *AbortSignal, emit Emit) error {
		for i := 0; ; i++ {
			if err := emit(Chunk{Seq: i, Data: []byte{byte(i)}}); err != nil {
				return err
			}
			emitted++
		}
	}

	writes := 0
	sink := SinkFunc(func(c Chunk) error {
		writes++
		if writes == failAt {
			return wantErr
		}
		return nil
	})

	res, err := Run(context.Background(), producer, sink)
	if !errors.Is(err, wantErr) {
		t.Fatalf("run=%s expected sink error, got %v", id, err)
	}
	// Exactly failAt-1 chunks were accepted by the sink.
	if res.ChunksDelivered != failAt-1 {
		t.Fatalf("run=%s ChunksDelivered=%d want %d (counting emits not deliveries?)",
			id, res.ChunksDelivered, failAt-1)
	}
	if !res.UpstreamAborted {
		t.Fatalf("run=%s sink error did not abort upstream", id)
	}
	t.Logf("run=%s sink-side count OK: delivered=%d (producer emitted ~%d)",
		id, res.ChunksDelivered, emitted)
}
