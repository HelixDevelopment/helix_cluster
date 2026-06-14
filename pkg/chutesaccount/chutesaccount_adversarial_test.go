package chutesaccount

// Adversarial / contract-pinning suite for the chutesaccount MONEY surface.
//
// FINDING (recorded honestly up front): chutesaccount is a READ-ONLY HTTP
// client. The package exposes NO money-mutation surface — there is no
// Credit/Debit/Charge, no shared balance accumulator, no overdraft/limit
// logic, and no balance arithmetic. `Account.Balance` is parsed once from the
// /users/me JSON and never mutated. `Client` is immutable after NewClient and
// holds no shared mutable state.
//
// Consequently the classic money-bug classes named in the brief have NO SINK
// in this package:
//   (a) conservation-under-race   — no shared accumulator to lose updates from;
//   (b) numeric poison of a balance — no arithmetic that NaN/Inf/overflow can
//       poison; the only numeric touchpoint is JSON decode;
//   (c) overdraft / limit bypass   — no debit path and no limit to bypass;
//   (d) double-spend / idempotency — no apply/charge path to replay.
//
// Rather than fabricate a sink, this file pins the contracts that DO exist and
// that a future refactor (e.g. adding caching, a shared balance field, or a
// `< 0` "sanitiser") could silently break. Every assertion is SINK-SIDE:
// the value the caller actually receives must EXACTLY equal what the server
// sent. These are the nearest real analogs to the brief's classes:
//
//   * TestConcurrentGetAccountNoTornReads  -> conservation-under-race analog:
//       under -race, N concurrent GetAccount calls must EACH return the exact
//       server balance. If a future refactor introduces a shared mutable
//       balance field, the race detector and the exact-equality assertion bite.
//   * TestBalanceFidelityExactRoundTrip    -> numeric-poison / hidden-<0-guard
//       analog: a NEGATIVE balance (real: account in debt), a large value, and
//       a high-precision value must round-trip BYTE-FOR-BYTE through decode.
//       A `< 0` clamp or rounding "sanitiser" is caught here.
//
// All tests below PASS on unmodified production code (NO BUG). They are
// regression guards, not failing reproducers.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// newBalanceServer returns an httptest.Server that serves /users/me with the
// given balance (enforcing the Authorization header via the existing
// authMiddleware so we exercise the real request path).
func newBalanceServer(t *testing.T, apiKey string, balance float64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/users/me", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Encode the float ourselves so we control the exact wire form and
		// there is no ambiguity about what the server "sent".
		fmt.Fprintf(w, `{"balance":%s}`, strconvFloat(balance))
	})
	return httptest.NewServer(authMiddleware(apiKey, mux))
}

// strconvFloat renders a float64 in a JSON-number form that decodes back to the
// identical bit pattern for finite values, using the shortest round-trippable
// representation Go's json package itself would accept.
func strconvFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// ----------------------------------------------------------------------------
// Conservation-under-race ANALOG: concurrent reads must not tear.
// ----------------------------------------------------------------------------

// TestConcurrentGetAccountNoTornReads is the closest real analog to the
// brief's "conservation under race" probe that this read-only package admits.
//
// There is no shared accumulator today, so this CANNOT lose an update — but it
// is the exact test that would BITE if a future change cached the balance on
// the Client (a shared mutable field) without synchronisation. Run with -race.
//
// SINK-SIDE assertion: every one of N concurrent GetAccount calls returns the
// EXACT server balance. Not "no panic" — exact equality on the returned money.
func TestConcurrentGetAccountNoTornReads(t *testing.T) {
	const (
		apiKey   = "cpk_adv_concurrent"
		balance  = 1234.5678
		nGorouts = 256
	)

	srv := newBalanceServer(t, apiKey, balance)
	defer srv.Close()

	client := NewClient(srv.URL, apiKey)

	var wg sync.WaitGroup
	results := make([]float64, nGorouts)
	errs := make([]error, nGorouts)

	wg.Add(nGorouts)
	for i := 0; i < nGorouts; i++ {
		go func(idx int) {
			defer wg.Done()
			acct, err := client.GetAccount(context.Background())
			errs[idx] = err
			results[idx] = acct.Balance
		}(i)
	}
	wg.Wait()

	for i := 0; i < nGorouts; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: GetAccount error: %v", i, errs[i])
		}
		// SINK-SIDE: exact equality. A torn read or shared-state corruption
		// would yield a wrong balance here even with -race "clean".
		if results[i] != balance {
			t.Fatalf("goroutine %d: balance = %v, want EXACTLY %v (torn read / lost update)",
				i, results[i], balance)
		}
	}
}

// ----------------------------------------------------------------------------
// Numeric-poison / hidden-`<0`-guard ANALOG: exact balance fidelity on decode.
// ----------------------------------------------------------------------------

// TestBalanceFidelityExactRoundTrip pins that GetAccount surfaces the server's
// balance EXACTLY, with no clamping, no `< 0` filtering, and no rounding.
//
// A negative balance is a legitimate state (account in debt / owes money). If a
// future "sanitiser" clamped negatives to 0 or rounded the value, money would
// be silently created or destroyed at the client boundary — the brief's
// conservation-violation class, applied to the only numeric sink that exists.
func TestBalanceFidelityExactRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		balance float64
	}{
		{"positive", 42.75},
		{"zero", 0.0},
		{"negative_in_debt", -19.99},          // must NOT be clamped to 0
		{"large", 9_999_999_999.99},           // must NOT overflow/round away
		{"tiny_precision", 0.000000123456789}, // must NOT be flushed to 0
		{"large_negative", -1_000_000.5},      // debt at scale
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			const apiKey = "cpk_adv_fidelity"
			srv := newBalanceServer(t, apiKey, tc.balance)
			defer srv.Close()

			client := NewClient(srv.URL, apiKey)
			acct, err := client.GetAccount(context.Background())
			if err != nil {
				t.Fatalf("GetAccount(%s): unexpected error: %v", tc.name, err)
			}

			// SINK-SIDE: the caller's balance must equal the server's wire
			// value bit-for-bit. No clamp, no round, no sign flip.
			if acct.Balance != tc.balance {
				t.Fatalf("balance = %v, want EXACTLY %v (clamp/round/sanitiser detected)",
					acct.Balance, tc.balance)
			}
			// Explicitly assert a negative balance survives — the `< 0` guard
			// class. If someone adds `if b < 0 { b = 0 }`, this bites.
			if tc.balance < 0 && acct.Balance >= 0 {
				t.Fatalf("negative balance %v was clamped to %v: money destroyed at client boundary",
					tc.balance, acct.Balance)
			}
		})
	}
}

// TestBalanceNaNInfWireValuesRejectedNotPoisoned documents the numeric-poison
// class for the only numeric sink (JSON decode). JSON has no literal for NaN or
// ±Inf, so a hostile server cannot inject one through a standards-compliant
// body: encoding/json rejects "NaN"/"Inf" tokens with a decode error rather
// than silently yielding a poisoned float. We assert the SAFE outcome — an
// error is returned and NO poisoned Account leaks to the caller — so that if a
// future change swapped in a lenient decoder that accepted NaN/Inf, this bites.
func TestBalanceNaNInfWireValuesRejectedNotPoisoned(t *testing.T) {
	const apiKey = "cpk_adv_naninf"

	// These are the tokens a malicious server might try. Standard encoding/json
	// rejects all of them.
	for _, body := range []string{
		`{"balance":NaN}`,
		`{"balance":Inf}`,
		`{"balance":-Inf}`,
		`{"balance":Infinity}`,
		`{"balance":1e999}`, // overflows float64 -> +Inf on lenient parsers
	} {
		body := body
		t.Run(body, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/users/me", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, body)
			})
			srv := httptest.NewServer(authMiddleware(apiKey, mux))
			defer srv.Close()

			client := NewClient(srv.URL, apiKey)
			acct, err := client.GetAccount(context.Background())

			if err != nil {
				// Expected for NaN/Inf/Infinity tokens: decode error, no leak.
				return
			}
			// If decode SUCCEEDED, the balance MUST be finite. A poisoned
			// (NaN/±Inf) balance reaching the caller is the bug we guard against.
			if math.IsNaN(acct.Balance) || math.IsInf(acct.Balance, 0) {
				t.Fatalf("poisoned balance leaked to caller from body %q: got %v",
					body, acct.Balance)
			}
		})
	}
}
