# Security Scanning (Token-Free, 100% FOSS, No-CI)

| Field | Value |
|---|---|
| Status | active |
| Scope | Local, operator-run security gate for HelixCluster |
| Cost | $0 — all open-source, no paid subscription, no account token required (Sonar CE token is free + local) |

## Why this exists / how it fits the project rules

HelixCluster forbids CI/CD pipelines (constitutional Hard Stop). So — exactly like
`make sbom` and `make deps-update` — security scanning is a **local, operator-run
make target**, never a GitHub Action. You run it manually or on a periodic
reminder; it is intentionally not wired into any automated gate.

Every scanner below is **open-source and token-free**. There is **no paid Snyk /
SonarCloud subscription** and **no account token**:

- **Snyk is deliberately NOT used** — even its free tier needs a Snyk *account*
  token, so it is not token-free.
- The FOSS substitutes cover the same ground: **gosec** (SAST), **govulncheck**
  (Go vuln/SCA, reachability-aware), **trivy** (filesystem dependency + config +
  secret CVE scan).
- The optional **SonarQube Community Edition** is open-source and self-hosted;
  its scanner token is *minted on your own local server* — free local auth, not
  a SonarCloud account and not a paid tier.

The host is **Podman-only** (no Docker). Trivy and the Sonar scanner run as
Podman containers; the Go tools run from `~/go/bin` (installed via `go install`,
no `sudo`).

## Quick start

```bash
# Token-free FOSS scan (govulncheck + gosec + trivy). Requires a running
# podman machine for the trivy step (Go tools have no such requirement).
make security-scan
```

Evidence (sink-side proof per CLAUDE-1) is written to:

```
qa-results/security/<run-id>/        # run-id = UTC timestamp, e.g. 20260604T071535Z
  ├─ SUMMARY.txt                     # scanners-ran / with-findings / skipped counts + artifact list
  ├─ govulncheck-*.txt / *.json      # one pair per go.work module (. api/v1 security)
  ├─ gosec.txt / gosec.json          # Go SAST report (text + machine-readable)
  ├─ trivy-fs.txt / trivy-fs.json    # dependency + config + secret CVE report
  └─ *.skip                          # honest SKIP-with-reason for any tool that could not run
```

`qa-results/` is gitignored — evidence is local and never committed. The trivy DB
cache lives under `qa-results/.trivy-cache/` (also gitignored) so repeat runs do
not re-download the vulnerability DB.

## The scanners

### 1. govulncheck — Go vuln / SCA (reachability-aware)
- Run per `go.work` module: `.`, `api/v1`, `security`.
- Auto-installed if missing: `go install golang.org/x/vuln/cmd/govulncheck@latest`.
- Reports only advisories whose vulnerable symbols are **actually reachable** from
  your code, so it is the lowest-noise SCA signal. (Already also wired into
  `scripts/deps-update.sh`.)

### 2. gosec — Go SAST
- Auto-installed if missing: `go install github.com/securego/gosec/v2/cmd/gosec@latest`.
- Walks the whole workspace; vendored / submodule / generated trees are excluded
  via `-exclude-dir` (`vendor,docs_chain,.git,bin,build,dist,sbom,qa-results,node_modules`).
- Emits both `gosec.json` (machine-readable, grouped by `rule_id`) and `gosec.txt`.

### 3. trivy — filesystem dependency + config + secret CVE scan (via Podman)
- Runs as `podman run --rm -v $PWD:/src:z docker.io/aquasec/trivy:latest fs ...`
  with `--scanners vuln,misconfig,secret`.
- A native `trivy` on PATH is used instead when present (`--cache-dir` set either
  way).
- If neither native trivy nor a reachable Podman engine is available, the step
  emits an **honest SKIP** (a `trivy.skip` file + summary line) — never a fake
  PASS. A trivy run that fails to produce a valid `Results` report is likewise
  treated as a SKIP/failure, not as "no findings".

> Note on govulncheck vs. trivy counts: govulncheck is **reachability-aware** and
> may report zero while trivy lists CVEs present in `go.mod`. That is expected —
> trivy flags a vulnerable dependency *version* regardless of whether the
> vulnerable code path is reachable. Use govulncheck to prioritise (reachable =
> urgent) and trivy for full dependency hygiene / version hygiene.

## Exit status

`make security-scan` exits:

- `0` — at least one scanner ran and none reported findings.
- `1` — one or more scanners reported findings (review the evidence).
- `2` — no scanner could run at all (install the FOSS tools / start Podman).

Honest SKIPs alone do not fail the run; they are recorded in `SUMMARY.txt`.

## Optional: self-hosted SonarQube Community Edition (free)

SonarQube CE is open-source (LGPL) and self-hostable for free. This is **not a
paid subscription** and **not SonarCloud** — you run your own server and mint a
token on it for the scanner. That token is free local auth.

Compose file: `deploy/compose/security_sonarqube.yml` (driven with
`podman-compose`). It pins `sonarqube:community` and **disables the embedded
Elasticsearch bootstrap check** (`SONAR_ES_BOOTSTRAP_CHECKS_DISABLE=true`) so the
server starts on a low-RAM dev box / podman-machine without `sysctl` tuning (no
`sudo`). For a real production deployment you would instead raise
`vm.max_map_count` on the host.

```bash
make sonar-up                 # start the local CE server (Podman)
# open http://localhost:9000  (first login admin/admin → you must set a new password)
# mint a token: My Account > Security > Generate Tokens
export SONAR_TOKEN=<your-local-token>
make sonar-scan               # runs scripts/security/sonar-scan.sh
make sonar-down               # stop the server (never leaves a container up)
```

`make sonar-scan` is **gated on `SONAR_TOKEN`**: if it is unset, the script prints
the free-token mint instructions and SKIPs (exit 3) rather than faking a PASS.
Scanner output is captured to `qa-results/security/sonar-<run-id>/sonar-scanner.log`.
The scanner reaches the server via `host.containers.internal` from inside the
Podman container; override with `SONAR_HOST_URL` / `SONAR_PROJECT_KEY` if needed.

## Files

| Path | Purpose |
|---|---|
| `Makefile` | `security-scan`, `sonar-up`, `sonar-down`, `sonar-scan` targets |
| `scripts/security/security-scan.sh` | Token-free FOSS scan (govulncheck + gosec + trivy) + evidence capture |
| `scripts/security/sonar-scan.sh` | SonarQube CE analysis (gated on `SONAR_TOKEN`) |
| `deploy/compose/security_sonarqube.yml` | Self-hosted SonarQube CE (Podman, ES bootstrap check disabled for dev) |

## Triage notes (interpreting findings)

- **gosec** `G104` (unhandled errors) and `G115` (integer overflow conversions)
  are the highest-volume rules and are typically the lowest-risk; prioritise
  `G101` (hardcoded creds), `G204/G702` (command injection), `G304/G703` (path
  traversal), and `G404` (weak RNG where it guards security).
- **trivy secrets**: verify each hit — placeholder/sentinel tokens used in tests
  and QA transcripts (e.g. `*-must-not-leak-*` strings that *assert* secrets do
  NOT leak) are false positives.
- **trivy CRITICAL/HIGH vulns** concentrated in vendored submodules reflect the
  submodule's own (older) `go.mod`; remediate by updating the submodule or its
  pinned deps upstream, not by editing vendored trees.
- Remediation/triage of findings is a **follow-up work unit**; `make
  security-scan` only captures and summarises honestly.
```
