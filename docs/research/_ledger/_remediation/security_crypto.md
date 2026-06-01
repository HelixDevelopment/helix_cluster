# §7.1 Anti-Bluff Remediation — package group `security_crypto`

| Field | Value |
|---|---|
| Packages | `pkg/security`, `pkg/crypto`, `pkg/jwt` |
| Risk (audit) | MEDIUM (security) / LOW (crypto) + JWT (not separately rated) |
| Date | 2026-06-01 |
| Self-verify | `build=ok vet=ok race=ok` (each package, `-race -count=1`) |
| Tests added | 3 (each §7.1-evidence-backed + mutation-proven) |
| Files changed | 3 (all NEW test files; no production edits required) |
| Pending forensics | 1 (Vault production client — external infra) |

---

## 1. Audit findings reviewed (file:line)

The AUDIT_REPORT.md `security` (MEDIUM) row claimed three bluffs:

1. **mTLS never handshakes** — "tests only inspect `*tls.Config` fields, never prove a valid client connects and an invalid client is rejected." (`pkg/security/tls_test.go`)
2. **Vault is mock-only; no production `VaultClient` exists in-package.** (`pkg/security/vault.go:17`)
3. **`TestNewTLSConfigBuilder` is non-nil-only.** (`pkg/security/tls_test.go:70`)

The `crypto` (LOW) row: "`Hash`/`GenerateKey` assert length only; sentinels never checked via `errors.Is`; only AES-256 covered." (`pkg/crypto/package_test.go`)

JWT was not separately rated but the task mandates a proven sign→verify→tamper lifecycle.

### State found on entry (prior remediation already present)

A previous remediation pass had **already** closed most of the raw audit gaps in this group:

- `pkg/security/tls_test.go:228-375` already contains real `tls.Listen`/`tls.DialWithDialer` handshake tests: valid same-CA client succeeds and round-trips a byte; no-cert and foreign-CA clients are rejected via real I/O (`TestMTLSHandshake_ValidClientSucceeds`, `_NoClientCertRejected`, `_ForeignClientCertRejected`). `MinVersion==TLS13` is asserted on both server and client configs.
- `pkg/crypto/package_test.go` already has SHA-256 KAT vectors (`TestHash_KnownAnswer`), `GenerateKey` KAT, `errors.Is` checks on all three sentinels, AES-128/192/256 coverage, wrong-key, short-ciphertext, tampered-ciphertext, and a PBKDF2 KAT.
- `pkg/jwt/package_test.go` already has HMAC/RSA tamper mutation tests.

**However**, none of those tests used the Constitution §7.1 evidence helper (`pkg/testing/evidence`) — no per-run token embedded into the action and recovered from the sink, no state-delta proof, no captured manifest/artifacts. The task explicitly requires: (a) a real mTLS handshake that **asserts the negotiated peer identity**; (b) a crypto round-trip with a **per-run token recovered**; (c) JWT tamper-detection — all with §7.1 evidence. The existing mTLS test asserts only `len(PeerCertificates) != 0`, never the negotiated identity. That residual gap is what this remediation closes.

---

## 2. Changes, file by file

All changes are **new test files**. No production code needed modification — the production primitives (`ServerTLS`/`ClientTLS`, `Encrypt`/`Decrypt`, `GenerateToken`/`ValidateToken`/`ParseToken`) are genuinely functional; the bluff was the absence of evidence-backed, identity-asserting proof, which is now supplied.

### `pkg/security/tls_evidence_test.go` (NEW)
- `generateIdentityCerts` mints a CA + server cert (CN `test-server`, SAN `localhost`) + a client cert whose **CommonName embeds the per-run evidence token**, all under one CA.
- `TestMTLS_NegotiatedPeerIdentity_Evidence`: stands up a real `tls.Listener` with `ServerTLS()`, dials with `ClientTLS()`, forces the handshake, and the **server publishes the verified client identity** it negotiated from `ConnectionState().VerifiedChains[0][0].Subject.CommonName`. Then:
  - `e.MustDelta("peer-identity-negotiated", "", negotiatedCN)` — server went from no peer identity to the token-bearing identity.
  - `e.MustPositive("peer-cn-contains-token", negotiatedCN, token)` — the negotiated identity carries the unique per-run token (proves the server read the REAL client cert via the verified chain, not an echoed value).
  - Asserts `negotiatedCN == clientCN`, `state.Version == TLS13`, and writes `negotiated_peer_cn.txt` artifact + manifest.

### `pkg/crypto/package_evidence_test.go` (NEW)
- `TestEncryptDecrypt_TokenRoundTrip_Evidence`: embeds the per-run token into the plaintext, `Encrypt` (AES-256-GCM) → `Decrypt`, then:
  - `MustDelta` ciphertext != plaintext (no echo/no-op cipher).
  - Negative confinement: the token must **not** appear in the ciphertext.
  - `MustPositive("decrypted-contains-token", recovered, token)` — the per-run token survives the full round-trip.
  - Writes `ciphertext.hex` + `recovered.txt` artifacts + manifest.

### `pkg/jwt/package_evidence_test.go` (NEW)
- `TestJWT_SignVerifyTamper_Evidence`: puts the per-run token in the `jti` claim, `GenerateToken` (HS256), `ParseToken` recovers it (`MustPositive`), then forges the payload (`jti = attacker-<token>`) **without re-signing** and asserts `ValidateToken(forged)` is REJECTED and `ParseToken(forged)` returns no claims.
  - `MustDelta("tamper-detection-validity-flip", genuineValid, forgedAccepted)` — validity flips genuine(valid)→forged(rejected).
  - Writes `signed_token.jwt` + `forged_token.jwt` artifacts + manifest.

---

## 3. Behaviors now PROVEN (and how)

| Behavior | Proof mechanism | Anti-bluff guarantee |
|---|---|---|
| Real mTLS handshake with **negotiated peer identity** | Live `tls.Listen`+`tls.DialWithDialer`; server reads verified peer CN from `VerifiedChains`; per-run token embedded in client CN and recovered server-side | Mutation: `ClientAuth=NoClientCert` → server learns no identity → `MustDelta` FAILS |
| AES-GCM encrypt→decrypt **per-run token recovery** | Token in plaintext, recovered via `Decrypt`; token absent from ciphertext | Mutation: `Decrypt` returns constant → `MustPositive` FAILS |
| JWT sign→verify→**tamper rejection** | Genuine token verifies & recovers token; payload-forged token rejected | Mutation: skip `hmac.Equal` → forged accepted → test FAILS with SECURITY message |

### Mutation evidence (executed during remediation)
- crypto `Decrypt`→constant: `FAIL ... positive evidence "<token>" not found in result` ✓
- jwt skip signature check: `FAIL ... SECURITY: forged (tampered) token was accepted` ✓
- security `ClientAuth=NoClientCert`: `FAIL ... no state delta — before==after ()` ✓
- All three pass after revert. The tests are non-tautological.

---

## 4. Exact self-verify results

Command (per the task, YOUR packages only, `-race -count=1`):

```
build=ok vet=ok race=ok
```

Individual package output (`go test -race -count=1`):
```
ok  github.com/HelixDevelopment/helix_cluster/pkg/security  ~1.5s
ok  github.com/HelixDevelopment/helix_cluster/pkg/crypto    ~1.8s
ok  github.com/HelixDevelopment/helix_cluster/pkg/jwt       ~1.6s
```

---

## 5. Integration tests for the orchestrator to run

None added in this pass. All three proven behaviors run **without external infra** (in-process `tls.Listener`/`Dial`, pure crypto, pure JWT) and are therefore genuinely real in the non-integration path — no testcontainers/etcd/Vault needed. No `//go:build integration` files were created.

---

## 6. PENDING_FORENSICS

- **Vault production client (`pkg/security/vault.go`).** The audit's second bluff — "no production `VaultClient` exists in-package (mock-only)" — remains **structurally unresolved**. `vault.go` defines the `VaultClient` interface + `VaultWrapper` + `MockVaultClient`, and `vault_test.go` exercises the wrapper only through the mock. A genuine KVv2 round-trip / PKI issue→`x509.ParseCertificate` proof requires a **real dev-mode HashiCorp Vault server**, which is external infra this subagent must not start, and a real implementation would require a new dependency (`github.com/hashicorp/vault/api`) — forbidden (no `go.mod`/`go.sum` edits). 
  - **Precise reason:** cannot add the Vault API dependency (go.mod is off-limits) and cannot start a Vault container/server. Writing a "passing" Vault integration test against only the mock would itself be a §7.1 PASS-bluff, so it was deliberately NOT written.
  - **Hand-off for orchestrator:** add `github.com/hashicorp/vault/api`, implement a `realVaultClient` satisfying `VaultClient`, and add a `//go:build integration` test: `vault server -dev` → `WriteSecret`/`ReadSecret` KVv2 round-trip (assert a per-run token recovered) and `IssueCertificate` → `pem.Decode` → `x509.ParseCertificate` (assert CN/serial present). Command once deps exist: `go test -tags=integration -race -run Vault ./pkg/security/...` with a dev Vault at `VAULT_ADDR`.

---

## 7. Constitution compliance note

Per CLAUDE-1 rule 3 ("every test MUST prove the feature works for end users... verify sink-side behavior") and §7.1: each new test embeds a unique per-run token into the real action and recovers it from the sink (negotiated peer CN / decrypted plaintext / verified jti claim), proves a state delta, and writes a forensic manifest + artifacts under `t.TempDir()/<feature>/<run-id>/`. Each was mutation-validated to FAIL against a deliberately broken implementation, satisfying the "tests are necessary but NOT sufficient" mandate.
