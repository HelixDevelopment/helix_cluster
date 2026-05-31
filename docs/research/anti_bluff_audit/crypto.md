# pkg/crypto — Anti-Bluff Audit

- **Test result:** PASS — 19/19 tests pass (`go test ./pkg/crypto/... -count=1 -v`, ok in 1.537s)
- **Risk:** LOW
- **Real-behavior coverage:** The package wraps Go's standard `crypto` primitives (SHA-256, Ed25519, AES-GCM, PBKDF2-SHA256) — no mocks or stubs anywhere; every test exercises the real implementation. The tests provide genuine sink-side verification, not just `err==nil`:
  - **AES-GCM round-trip** (`TestEncryptDecrypt`, line 87): decrypts and asserts `bytes.Equal(decrypted, plaintext)` — proves the actual produced ciphertext is recoverable, not merely that no panic occurred.
  - **Authenticated-encryption integrity** (`TestDecryptTamperedCiphertext`, line 123): flips a byte and asserts decryption FAILS — proves GCM auth tag is actually enforced (failure-path coverage).
  - **Nonce uniqueness** (`TestEncryptNonceUniqueness_Mutation`, line 197): same plaintext encrypted twice must differ — proves a fresh random nonce is really used (would catch a hardcoded/zero nonce).
  - **Ed25519 sign/verify** (`TestSignVerify`, line 38) plus two distinct negative paths: tampered message (`TestVerifyBadSignature`, line 58) and wrong public key (`TestVerifyBadKey`, line 69) — both assert verification fails, proving the signature is genuinely bound to message and key.
  - **Invalid-key guard** (`TestSignInvalidKey`, line 80) exercises the size-validation error path.
  - **PBKDF2 determinism + differentiation** (`TestDeriveKey` line 146 with a real 100k-iteration run; `TestDeriveKeyDifferentInputs` line 160): same inputs → identical key, different password → different key, proving derivation actually depends on inputs.
  - **Concurrency** (`TestEncryptDecryptConcurrent_Mutation`, line 207): 100 goroutines round-trip distinct plaintexts with one shared key and verify equality.
  - Mutation-paired tests exist for Hash determinism, Hash input-sensitivity, key length, nonce uniqueness, and derive-key length per Constitution §1.1 — each would fail if the corresponding behavior were removed.

- **PASS-bluff findings:** Mostly clean. Minor weak spots (none rise to HIGH):
  - `TestHash` (line 11) and `TestGenerateKey` (line 18) assert only output length, not a known-answer value. A broken hash returning a fixed 64-char string would still pass these two. Mitigated by `TestHash_DifferentInput_Mutation` (line 178) which proves input-sensitivity, but no test pins SHA-256 against a published KAT vector, so a wrong-but-deterministic digest algorithm would not be detected.
  - `TestEncryptDecryptEmptyPlaintext` (line 108) asserts `len(decrypted)==0` — correct, but the empty-input case is the weakest possible sink check.
  - Swallowed errors in negative-path setup: `TestVerifyBadSignature` (line 59), `TestVerifyBadKey` (line 70), `TestDecryptTamperedCiphertext` (line 126), and `TestEncryptNonceUniqueness_Mutation` (lines 200-201) discard the `err` from `GenerateKeyPair`/`Sign`/`Encrypt` with `_`. Not a behavior bluff (the assertion that follows is real), but a setup failure would surface confusingly rather than as a clean fail.
  - No test asserts the specific sentinel errors (`ErrVerifyFailed`, `ErrInvalidKey`, `ErrInvalidCiphertext`) via `errors.Is` — only that *some* error is returned. Error-contract coverage is shallow.
  - No coverage of AES-128/AES-192 key sizes (only 32-byte AES-256) and no test for the "ciphertext too short" branch (`Decrypt`, line 108-110) nor for `Encrypt`/`Decrypt` rejecting an invalid key length.

- **Recommended hardening:**
  1. Add SHA-256 known-answer assertions, e.g. `Hash([]byte("abc")) == "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"`, to defeat a deterministic-but-wrong digest.
  2. Replace `err` discards (`_`) in negative-path setup with `t.Fatalf` on error so setup failures are unambiguous.
  3. Assert sentinel errors with `errors.Is(err, ErrVerifyFailed)` / `ErrInvalidKey` / `ErrInvalidCiphertext` to lock the error contract.
  4. Add table-driven coverage for AES-128 (16-byte) and AES-192 (24-byte) keys, plus an explicit "wrong/invalid key length rejected" case for `Encrypt`/`Decrypt`.
  5. Add a "ciphertext too short" test (hex shorter than `gcm.NonceSize()`) to cover the `ErrInvalidCiphertext` length branch in `Decrypt`.
  6. Add a wrong-key decrypt test (encrypt with key A, decrypt with key B → auth failure) to prove key binding for AES-GCM, mirroring the Ed25519 wrong-key test.
  7. Consider a KAT/cross-check for `DeriveKey` (e.g. a fixed PBKDF2 vector) so the derivation cannot silently change algorithm/parameters.
