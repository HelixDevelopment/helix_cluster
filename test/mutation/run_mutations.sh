#!/usr/bin/env bash
# Helix Cluster OS — §1.1 Meta-Test Mutation Runner
#
# Constitution §1.1 (paired mutation): a test is only trustworthy if a
# corresponding mutation of the code-under-test flips it PASS -> FAIL. A test
# that still passes after its guard/atomic/branch is removed is a PASS-BLUFF
# (CLAUDE-1) and is reported as a SURVIVING mutation (runner exits non-zero).
#
# This runner is also the LOCAL PROOF backing the fleet-wide -race CI gate
# (HXC-1074): the metrics/errors cases remove an atomic/lock and require the
# race detector to trip, demonstrating the gate catches the exact class of
# defect the audit flagged.
#
# Mechanism per case: snapshot file -> apply literal mutation -> run the paired
# test -> assert it FAILED (mutation caught) -> restore file from snapshot.
# Snapshots (not git) are used so the runner is safe on a dirty tree; a trap
# restores every file on any exit.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "${ROOT}"

# Per-run UUID for forensic evidence (§7.1).
RUN_ID="$(uuidgen 2>/dev/null || python3 -c 'import uuid;print(uuid.uuid4())')"
EVID_DIR="qa-results/mutation/${RUN_ID}"
mkdir -p "${EVID_DIR}"
LOG="${EVID_DIR}/run.log"

# Mutation cases: id | file | literal-find | literal-replace | pkg | test-regex | race(0|1)
# Each find string MUST appear verbatim and uniquely-enough in the file.
CASES=(
  "metrics-atomic|pkg/metrics/package.go|atomic.AddInt64(&c.val, 1)|c.val += 1|./pkg/metrics|TestCounter_ConcurrentInc_Mutation|1"
  "errors-rlock|pkg/errors/package.go|e.mu.RLock()|// e.mu.RLock()|./pkg/errors|TestConcurrentFieldAccess|1"
  "ratelimit-underfill|pkg/ratelimit/ratelimit.go|tokens:   float64(max),|tokens:   float64(max - 1),|./pkg/ratelimit|TestTokenBucket_StartsFull_Mutation|0"
  "semaphore-overcap|pkg/semaphore/package.go|make(chan struct{}, n)|make(chan struct{}, n+1)|./pkg/semaphore|TestSemaphore_ZeroCapacity_Barrier|0"
  "workerpool-norecover|pkg/workerpool/package.go|_ = recover()|_ = 0 // recover removed by mutation|./pkg/workerpool|TestPool_PanickingTaskDoesNotKillPool|0"
)

declare -a SNAP_FILES=()
declare -a SNAP_BAKS=()
restore_all() {
  local i
  for i in "${!SNAP_FILES[@]}"; do
    [ -f "${SNAP_BAKS[$i]}" ] && cp -f "${SNAP_BAKS[$i]}" "${SNAP_FILES[$i]}"
  done
}
trap restore_all EXIT INT TERM

echo "=== §1.1 Mutation Runner — run ${RUN_ID} ===" | tee "${LOG}"
echo "root=${ROOT}" | tee -a "${LOG}"

caught=0; survived=0; errored=0
results=""

for case in "${CASES[@]}"; do
  IFS='|' read -r id file find repl pkg testre race <<< "${case}"
  echo "" | tee -a "${LOG}"
  echo "--- case ${id}: mutate ${file} :: '${find}' -> '${repl}' ---" | tee -a "${LOG}"

  if ! grep -qF -- "${find}" "${file}"; then
    echo "  ERROR: find-string not present in ${file} (drift?)" | tee -a "${LOG}"
    errored=$((errored+1)); results+="\n  [ERROR ] ${id}: find-string missing"
    continue
  fi

  bak="$(mktemp)"; cp -f "${file}" "${bak}"
  SNAP_FILES+=("${file}"); SNAP_BAKS+=("${bak}")

  FIND="${find}" REPL="${repl}" perl -0pi -e 'my $f=$ENV{FIND}; my $r=$ENV{REPL}; s/\Q$f\E/$r/g' "${file}"

  if [ "${race}" = "1" ]; then
    out="$(go test -race -count=1 -run "${testre}" "${pkg}" 2>&1)"; rc=$?
  else
    out="$(go test -count=1 -run "${testre}" "${pkg}" 2>&1)"; rc=$?
  fi
  echo "${out}" | tail -8 | sed 's/^/    /' | tee -a "${LOG}"

  cp -f "${bak}" "${file}"   # restore immediately

  if [ "${rc}" -ne 0 ]; then
    echo "  CAUGHT: paired test failed under mutation (rc=${rc}) — test is load-bearing" | tee -a "${LOG}"
    caught=$((caught+1)); results+="\n  [CAUGHT] ${id} (${testre})"
  else
    echo "  SURVIVED: paired test PASSED despite mutation — BLUFF (§1.1 violation)" | tee -a "${LOG}"
    survived=$((survived+1)); results+="\n  [SURVIVE] ${id} (${testre}) — BLUFF"
  fi
done

echo "" | tee -a "${LOG}"
echo "=== SUMMARY (run ${RUN_ID}) ===" | tee -a "${LOG}"
echo -e "${results}" | tee -a "${LOG}"
echo "caught=${caught} survived=${survived} errored=${errored} total=${#CASES[@]}" | tee -a "${LOG}"
echo "evidence: ${EVID_DIR}/run.log" | tee -a "${LOG}"

# §1.1 contract: every mutation must be CAUGHT. Any survivor or error fails the gate.
if [ "${survived}" -ne 0 ] || [ "${errored}" -ne 0 ]; then
  echo "RESULT: FAIL — surviving mutations or drift detected" | tee -a "${LOG}"
  exit 1
fi
echo "RESULT: PASS — all ${caught} mutations caught by their paired tests" | tee -a "${LOG}"
exit 0
