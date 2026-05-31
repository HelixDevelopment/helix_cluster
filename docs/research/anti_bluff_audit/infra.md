# pkg/infra — Anti-Bluff Audit

- **Test result:** PASS — 17/17 tests pass (`go test ./pkg/infra/... -count=1`, 0.564s).
- **Risk:** HIGH

- **Real-behavior coverage:**
  Genuinely proven only at the level of an **in-memory bookkeeping data structure**:
  - `NewOrchestrator`/`DefaultConfig` initialize non-nil maps and expected config literals.
  - Map insert/lookup/delete bookkeeping: `Boot` adds entries, `Stop` mutates them, `Status`/`Health` read them back, `VMSpawn`/`VMDestroy`/`VMList` add/remove map entries, and `VMSimulateFailure`/`VMSimulatePartition` mutate an in-map status string.
  - The partition auto-heal timer (`VMSimulatePartition`) is the only test that proves a real concurrent behavior: the goroutine flips status `partitioned -> running` after the timer fires, and the test observes both states. This is the single non-trivial behavioral assertion in the suite.
  - Not-found error paths are covered for `Logs`, `Status`, `VMDestroy`, `VMStatus`, `VMSSH`, `VMSimulateFailure`, `VMSimulatePartition`.

- **PASS-bluff findings:**
  The package CLAIMS (per doc comments and the project's stated stack of PostgreSQL/Redis/etcd/NATS/Kafka/VMs) to be an infrastructure orchestrator. In reality `orchestrator.go` performs **zero real-world operations**: no Docker/Compose, no process spawn, no network call, no SSH, no health probe. Every method just writes/reads hardcoded strings in a `map`. The tests then assert those same hardcoded strings — the definition of a PASS-bluff under CLAUDE-1 / PCS-6. Specific items:
  - `orchestrator.go:84-103` `Boot` — sets `Status:"running", Healthy:true, Message:"booted"` unconditionally and never starts anything. Test `orchestrator_test.go:36-38` asserts `"running"`/`Healthy==true`, which are literals the code always sets. **No sink-side verification** that any service (e.g. `postgres-primary`) actually accepts a connection. A real PostgreSQL never boots; the test still passes.
  - `orchestrator.go:160-186` `Health` — sets `Latency = 1*time.Millisecond` for any known service and reports the stored `Healthy` (always true after Boot). Test `orchestrator_test.go:93-96` asserts `Healthy==true` — a tautology, since Boot hardcoded it true and Health never probes anything. **No failure-path:** there is no test where a service is unhealthy, because the code can never report a booted service as unhealthy.
  - `orchestrator.go:189-196` `Logs` — returns `nil` for any existing service and never streams or produces any log output (the `follow`/`tail`/`since`/`timestamps` params are all ignored). Test `orchestrator_test.go:105-106` only asserts `err==nil` — happy path checks nothing was produced. **Ignored params + no sink-side output check.**
  - `orchestrator.go:199-216` `Scale` — only writes `s.Replicas` and a formatted message; no container/replica is actually created. Test `orchestrator_test.go:118-121` asserts the int it just set (`Replicas==3`) and the message substring it just formatted — **asserts a literal it set** (tautological round-trip).
  - `orchestrator.go:219-239` `VMSpawn` — fabricates `CPU:2, Memory:4096, IP:10.0.0.N` with no hypervisor/cloud call. Test `orchestrator_test.go:131-134` asserts those exact hardcoded constants — **asserts literals the constructor hardcodes**; proves nothing about a real VM.
  - `orchestrator.go:274-282` `VMSSH` — returns a formatted `"ssh user@<ip>"` string; nothing is reachable. Test `orchestrator_test.go:186` only asserts the result `Contains "ssh"` (a substring of the format literal). **No proof the host is reachable / SSH works.**
  - `orchestrator.go:285-294` `VMSimulateFailure` — flips a string field; test `orchestrator_test.go:202` asserts `"failed"`. Round-trip of a literal. (The mutation-pair is weakly satisfied since removing the write would change the observed string, but it proves no real failure injection.)
  - Mutation-pairing (Constitution 1.1) is mostly **absent in spirit**: most assertions would still pass if the "real work" were removed because there is no real work — they only fail if the trivial map write is removed. There is no test that would fail if, say, the orchestrator silently failed to start a real service.
  - No `t.Skip` and no mocks-as-such — but the entire production type is itself a stub, which is worse: a mock at least signals it is fake; here the stub is shipped as the real implementation.

- **Recommended hardening:**
  Tests cannot become honest without the implementation doing real work. Concretely:
  1. **Integration tests against REAL services (CLAUDE-1.1):** use testcontainers-go (or a real docker-compose harness) so `Boot("postgres-primary")` actually starts Postgres, then assert sink-side: open a `pgx`/`database/sql` connection and run `SELECT 1`. For `redis-master` issue a `PING`; for `etcd` a member-list; for `kafka` a metadata/produce-consume round-trip.
  2. **Health must probe, not echo:** add a test where a service is started then killed/paused, and assert `Health` reports `Healthy==false` with a real latency measured from an actual probe (not the hardcoded `1ms`). Add the missing **failure-path** coverage.
  3. **Logs sink-side:** start a service that emits a known marker line, call `Logs(..., tail=N)` capturing output to a buffer, and assert the marker appears and that `tail`/`since`/`timestamps` actually affect the output.
  4. **Scale sink-side:** after `Scale(svc, 3)`, query the runtime (compose/docker API) and assert 3 running replicas exist — not just that the struct field equals 3.
  5. **VM/SSH end-to-end:** spawn a real (or firecracker/QEMU/local-container) node, then assert `VMSSH` output actually establishes a session (run `echo ok` over SSH and check stdout). For `VMSimulateFailure`/`VMSimulatePartition`, assert observable network effect (e.g. health probe to the node fails during partition, recovers after) rather than an internal string field.
  6. Add **mutation-paired** assertions that fail if the real effect is removed (e.g. a connectivity check that fails when the service was never actually started).
  7. Capture **sink-side evidence** (connection success log / metrics) per CLAUDE-1.6 before declaring the feature complete.
