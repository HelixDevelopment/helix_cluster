# pkg/pubsub — Anti-Bluff Audit

- **Test result:** PASS — 4/4 tests (`TestBroker`, `TestBroker_MultipleSubscribers_Mutation`, `TestBroker_WrongSubjectNotDelivered_Mutation`, `TestBroker_NonBlockingPublish_Mutation`), `go test ./pkg/pubsub/... -count=1 -v` ok in 0.350s.

- **Risk:** LOW

- **Real-behavior coverage:**
  The package is a small, fully self-contained in-memory pub/sub `Broker` (`package.go`, 37 lines). There is no external service, network, or persistence claimed — the "real" implementation IS the in-memory map of channels, and the tests exercise that real implementation directly (no mocks/stubs). What is genuinely proven:
  - **End-to-end delivery (sink-side):** `TestBroker` (test:8) subscribes, publishes, and asserts the *actual received message* off the channel equals `"hello"` — it verifies the produced effect, not just absence of panic.
  - **Fan-out to multiple subscribers:** `TestBroker_MultipleSubscribers_Mutation` (test:20) proves both channels on the same subject receive the message. This is a true mutation-paired test: if `Subscribe` overwrote instead of appended (`b.subjects[subject] = []chan string{ch}`), `ch1` would receive nothing and the test would fail.
  - **Subject isolation:** `TestBroker_WrongSubjectNotDelivered_Mutation` (test:44) proves a publish to `"politics"` is NOT delivered to a `"sports"` subscriber — a real failure-path/negative assertion. If `Publish` broadcast to all subjects, this fails.
  - **Non-blocking drop semantics:** `TestBroker_NonBlockingPublish_Mutation` (test:57) fills the 10-buffer channel and asserts a further publish does not block (500ms timeout guard). If the `select { case ch <- msg: default: }` drop were removed and `Publish` blocked on a full buffer, this test fails. Genuinely proves the documented non-blocking behavior.

  Each core behavior (delivery, fan-out, isolation, non-blocking drop) has a paired test that would fail if the behavior were removed, satisfying Constitution 1.1.

- **PASS-bluff findings:**
  - None of the four tests are pass-bluffs. No tautological assertions, no mock substitution, no swallowed errors, no `t.Skip`. Sink-side effects are verified in every test.
  - Minor gaps (completeness, not bluffs):
    - No concurrency/race test despite the `sync.RWMutex` being the package's main correctness mechanism. The mutex's real value (safe concurrent Subscribe/Publish) is unproven; tests run single-goroutine, so a broken lock would not be caught. Recommend running under `-race` with concurrent producers/consumers.
    - No test for an empty/unknown subject Publish (e.g. `Publish("never-subscribed", ...)` should be a safe no-op) — currently relies on Go's nil-map-read returning an empty slice; untested.
    - No test that a subscriber to subject A and a publisher to subject A interleaved with other subjects still isolates correctly beyond the single negative case.
    - `TestBroker_NonBlockingPublish_Mutation` leaves `ch` undrained (`_ = ch`), which is acceptable here but means the "fill" path isn't asserting buffer depth = exactly 10; the drop boundary (11th message dropped, 10th retained) is not explicitly verified.

- **Recommended hardening:**
  1. Add a `-race` concurrency test: spawn N goroutines subscribing and M goroutines publishing to overlapping subjects, then assert each subscriber receives a deterministic count; run the package with `go test -race`. This is the only way to honestly prove the `RWMutex` does its job.
  2. Add a boundary test asserting exactly the first 10 messages are retained and the 11th is dropped (drain the channel and count), making the buffer/drop contract explicit rather than only "does not block".
  3. Add a no-op test: `b.Publish("unsubscribed-subject", "x")` must not panic and must deliver nothing — locks in the nil/empty-subject path.
  4. (Optional) Document/test unsubscribe + channel-close lifecycle if the broker is intended for long-lived use; currently channels are never removed, a potential goroutine/memory leak that no test guards against.
