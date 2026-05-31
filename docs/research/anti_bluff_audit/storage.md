# pkg/storage — Anti-Bluff Audit

- **Test result:** PASS — 14/14 tests pass (`go test ./pkg/storage/... -count=1`), all in `package storage`.
- **Risk:** MEDIUM

- **Real-behavior coverage:** Genuinely good for the core happy paths. Both `MemoryStore` and `FileStore` are exercised through their REAL implementations — there are no mocks/stubs, so the "feature claims real-world operation" concern (CLAUDE-1 §5) is satisfied. Sink-side evidence is present in several tests:
  - `TestFileStoreNestedKey` (storage_test.go:150) does real `os.Stat` on the expected on-disk path `dir/deep/nested/key`, proving a real file is produced, not just that `Put` returned nil.
  - `TestFileStorePersistence` (storage_test.go:167) opens a SECOND `FileStore` over the same dir and reads the value back — proves data actually lands on disk and survives instance lifetime, a real end-user-visible effect.
  - `TestMemoryStorePutCopiesData` (storage_test.go:69) and `TestMemoryStoreGetCopiesData` (storage_test.go:80) mutate the caller's/returned buffer and re-read — these are real mutation-paired assertions that would FAIL if the defensive `copy()` in storage.go:45-47 / 59-61 were removed. Strong anti-bluff tests.
  - `TestMemoryStoreList` / `TestFileStoreList` assert exact key contents and ordering, not just `len>=0`.

- **PASS-bluff findings:**
  - **Sentinel errors never proven (error-path is a soft bluff).** Every "expected error" test (`TestMemoryStoreGetMissing` :24, `TestMemoryStoreEmptyKey` :32, `TestMemoryStoreDelete` :39, `TestFileStoreGetMissing` :109, `TestFileStoreEmptyKey` :182) only asserts `err == nil` → fail. None use `errors.Is(err, ErrKeyNotFound)` or `errors.Is(err, ErrEmptyKey)`. The package EXPORTS these sentinels (storage.go:14-17) as its public error contract, yet no test proves a caller can distinguish "not found" from "I/O error" or "empty key". A bug that returns the wrong wrapped error (or a generic error) would still PASS. This is the canonical "only checks err != nil" smell.
  - **FileStore.Put has NO defensive copy, and nothing tests it.** `MemoryStore` copies data in/out (and has paired tests), but `FileStore.Put` writes the caller slice directly (storage.go:111) and `Get` returns the freshly-read slice. The divergent aliasing/mutation semantics between the two `Store` implementations are entirely unproven — there is no `TestFileStore*CopiesData` analog. Callers relying on uniform `Store` behavior have no guarantee.
  - **Overwrite / update semantics untested (both stores).** No test does `Put(k, v1); Put(k, v2); Get(k)==v2`. Idempotent update is core KV behavior and is unverified; a `Put` that silently no-ops on existing keys would still pass every test.
  - **`Delete` of a missing key untested.** Both implementations deliberately return `nil` for a non-existent key (storage.go:68 unconditional `delete`; storage.go:137 `!os.IsNotExist`). This intentional idempotency is a behavioral contract with zero coverage — no mutation-paired test guards it.
  - **`List` edge cases untested.** Empty-prefix (`List("")` should return ALL keys) and non-matching-prefix (should return empty, not error) are never exercised. The `FileStore.List` directory-Stat branch (storage.go:151-154) computes `walkPrefix` but the result is never used to narrow the walk — dead/ineffective code that no test would catch.
  - **Concurrency unproven.** Both stores carry a `sync.RWMutex` (storage.go:29, 88) implying a thread-safety claim, but there is no concurrent/`-race` test. The locking could be removed and every test would still PASS — a textbook mutation-survivable gap per Constitution §1.1.

- **Recommended hardening (concrete):**
  1. Replace `err == nil` checks in the five error tests with `if !errors.Is(err, ErrKeyNotFound)` / `errors.Is(err, ErrEmptyKey)` to lock the exported error contract.
  2. Add `TestFileStorePutCopiesData` / `TestFileStoreGetCopiesData` OR document+assert that FileStore intentionally does not copy — make the two implementations' contract explicit and tested.
  3. Add an overwrite test for both stores: `Put(k,v1); Put(k,v2); assert Get(k)==v2`.
  4. Add `TestDeleteMissingKeyIsNoError` for both stores to guard the intentional idempotent-delete contract.
  5. Add `List("")` returns-all and `List("nomatch")` returns-empty-no-error tests; remove or test the ineffective dir-Stat branch in `FileStore.List`.
  6. Add a `-race` concurrent Put/Get/Delete test (N goroutines) so the `sync.RWMutex` claim is actually proven and mutation-detectable.
  7. Optionally add a table-driven `Store` conformance suite run against BOTH `NewMemoryStore()` and `NewFileStore(t.TempDir())` to guarantee interface-level parity.
