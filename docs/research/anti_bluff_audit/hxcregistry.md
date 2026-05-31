# pkg/hxcregistry — Anti-Bluff Audit

- **Test result:** PASS — 4 top-level tests (TestRegistryCRUD, TestRegistryNextID, TestItemValidation [5 sub-cases], TestRegistryConcurrentAccess), all passing via `go test ./pkg/hxcregistry/... -count=1`.

- **Risk:** MEDIUM

- **Real-behavior coverage (genuinely proven):**
  - Tests exercise the REAL implementation against a REAL SQLite engine (`modernc.org/sqlite`), not a mock/stub. This satisfies CLAUDE-1 §5 (no mock-only validation): the schema is actually created (`migrate()`), rows are really inserted, and CRUD goes through the real `database/sql` code path.
  - `TestRegistryCRUD` provides genuine sink-side verification: after `CreateItem` it re-reads via `GetItem` and asserts `Title`; after `UpdateItem("In progress")` it re-reads and asserts the persisted `Status` changed. This proves the write actually landed in the DB, not just that the call returned nil.
  - `ListItems("")` vs `ListItems("Queued")` proves the status filter WHERE-clause works (returns 1 for all, 0 for "Queued" after the item was moved to "In progress") — a real discriminating assertion, not a tautology.
  - `TestRegistryNextID` proves the HXC-id sequence increments against real data (HXC-001 on empty DB → HXC-002 after inserting HXC-001), exercising the `Sscanf`/`Sprintf` parse-and-increment logic.
  - `TestItemValidation` is mutation-paired for the validation rules it covers: each invalid case (missing id, invalid type/status/priority) would FAIL if the corresponding guard in `Validate()` were removed.

- **PASS-bluff findings:**
  - **`registry_test.go:38-40` — weak/tautological hash assertion.** Only asserts `got.HeadingHash != ""`. `ComputeHeadingHash` (item.go:56-60, a sha256 prefix) has NO test verifying its actual value or stability. The check would still pass if `EnsureHash` set the hash to any non-empty garbage, or if the hash were non-deterministic. The "stable hash for re-sync binding" claim (item.go:55) is unproven. No mutation-paired test for `ComputeHeadingHash`/`EnsureHash`.
  - **`item_history` side effect is never verified (registry.go:124-127, 189-191).** `CreateItem` and `UpdateItem` write an audit-history row, and those writes use SWALLOWED errors (`_, _ = r.db.Exec(...)`). No test ever queries `item_history`, so this feature is completely unproven — a classic PASS-bluff: the history feature could be entirely broken (or silently failing) and every test still passes.
  - **No failure-path coverage for the registry layer.** Happy-path only. Untested: duplicate-PK insert (second `CreateItem` with same `HXCID` should error), `GetItem` on a missing id (the `sql.ErrNoRows` → "not found" branch at registry.go:155-157 is never hit), `UpdateItem` on a non-existent id (currently a silent no-op — no test catches that gap), and DB CHECK-constraint violations.
  - **`TestRegistryConcurrentAccess` (registry_test.go:158-190) overclaims.** Named "ConcurrentAccess" but only spawns concurrent READS against a single pre-inserted row. It does NOT exercise concurrent writes, so it does not prove the registry is safe under write contention or that SQLite locking is handled — the risk the test name implies. It would pass even if writes were not concurrency-safe.
  - **`Validate()` does not enforce `CurrentLocation`, yet the DB does** (schema CHECK `current_location IN ('Issues','Fixed')`). No test inserts an invalid `CurrentLocation`, so the divergence between app-level validation and DB-level constraint is unproven; `phase`/`current_location` validation gaps are untested.
  - **`created_at`/`last_modified` round-trip not asserted.** `UpdateItem` bumps `LastModified`, but no test verifies the timestamp actually advanced or persisted; time fields are silently parsed with discarded errors (registry.go:161-162, 235-236).

- **Recommended hardening (concrete tests/assertions to add):**
  1. Assert the exact hash value and stability: `ComputeHeadingHash` of a known title equals a known 16-hex constant, and equals itself on repeated calls; assert two different titles produce different hashes (mutation-paired for the hashing logic).
  2. Verify the `item_history` sink: after `CreateItem`, query `item_history` and assert exactly one row with `event_type='Opened'`; after `UpdateItem`, assert an `Updated` row exists. Also stop swallowing those `Exec` errors (or at minimum assert via the query that they landed).
  3. Add failure-path tests: second `CreateItem` with a duplicate `HXCID` returns an error; `GetItem("HXC-999")` returns the "not found" error; `UpdateItem` on a non-existent id either errors or is asserted as a documented no-op (use `RowsAffected`).
  4. Make `TestRegistryConcurrentAccess` honest: run concurrent `CreateItem`/`UpdateItem` with distinct ids and assert all rows are present and consistent afterward (final count == N), or rename it to `TestRegistryConcurrentReads`.
  5. Assert `LastModified` strictly increased after `UpdateItem`, and that `CreatedAt` round-trips through the RFC3339 store/load.
  6. Add a DB-constraint test inserting an invalid `current_location` / `status` directly to prove the schema CHECK constraints are active (sink-side enforcement, not just Go-side `Validate`).
