// Package balancemonitor polls an OpenAI-style billing API for the USD account
// balance and records each reading together with a caller-supplied RunID.
//
// Design constraints (CLAUDE-1 / CLAUDE-2):
//   - Every Reading carries a RunID so tests can assert on real sink-side data.
//   - The wall clock is injected (nowFn) so tests are fully deterministic.
//   - HTTP is performed against a real (possibly in-process httptest) server;
//     there is no fake transport.
//   - LowBalance is set exactly when BalanceUSD < floorUSD (strict less-than).
//     A single-line mutation of that comparison breaks TestLowBalanceWarns.
package balancemonitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// ErrNoBalance is returned when the JSON response does not contain a
// recognisable balance field.
var ErrNoBalance = errors.New("balancemonitor: balance field absent from response")

// Reading is the result of a single balance poll.
type Reading struct {
	// BalanceUSD is the parsed USD balance returned by the billing endpoint.
	BalanceUSD float64

	// LowBalance is true when BalanceUSD is strictly below the configured
	// floorUSD.  The guard that sets this field is the mutation-kill target
	// for TestLowBalanceWarns — inverting or removing it makes that test fail.
	LowBalance bool

	// RunID is the identifier the caller passed to GetBalance; it travels
	// through to every recorded event so tests can assert on it.
	RunID string

	// At is the time the reading was captured (from the injected clock).
	At time.Time
}

// Event is a structured audit record written to the Monitor's in-process log.
type Event struct {
	Kind    string  // "reading" or "low_balance_warning"
	RunID   string
	Balance float64
	At      time.Time
}

// Monitor polls the balance endpoint and keeps an in-memory event log.
type Monitor struct {
	baseURL  string
	apiKey   string
	floorUSD float64
	nowFn    func() time.Time
	client   *http.Client

	mu     sync.Mutex
	events []Event
}

// NewMonitor builds a Monitor that will GET <baseURL>/billing/balance and
// compare the result against floorUSD.
//
// The real wall clock is used; inject a custom nowFn via newMonitorWithClock
// in tests.
func NewMonitor(baseURL, apiKey string, floorUSD float64) *Monitor {
	return newMonitorWithClock(baseURL, apiKey, floorUSD, time.Now, http.DefaultClient)
}

// newMonitorWithClock is the internal constructor that accepts a clock and an
// HTTP client so test code can inject deterministic time and an httptest server.
func newMonitorWithClock(baseURL, apiKey string, floorUSD float64, nowFn func() time.Time, client *http.Client) *Monitor {
	return &Monitor{
		baseURL:  baseURL,
		apiKey:   apiKey,
		floorUSD: floorUSD,
		nowFn:    nowFn,
		client:   client,
	}
}

// billingResponse covers both common OpenAI-style shapes:
//
//	{"total_available": 42.00}   (some billing APIs)
//	{"balance": 42.00}           (alternative field name)
//
// Unknown fields are silently ignored.
type billingResponse struct {
	Balance        *float64 `json:"balance"`
	TotalAvailable *float64 `json:"total_available"`
}

// GetBalance polls the /billing/balance endpoint, parses the USD amount,
// records a Reading (and optionally a low-balance warning) into the event log,
// and returns the Reading to the caller.
//
// The runID is embedded in every event record so callers can correlate log
// entries across concurrent runs.
func (m *Monitor) GetBalance(ctx context.Context, runID string) (Reading, error) {
	url := m.baseURL + "/billing/balance"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Reading{}, fmt.Errorf("balancemonitor: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return Reading{}, fmt.Errorf("balancemonitor: http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Reading{}, fmt.Errorf("balancemonitor: unexpected status %d: %s", resp.StatusCode, body)
	}

	var br billingResponse
	if err := json.NewDecoder(resp.Body).Decode(&br); err != nil {
		return Reading{}, fmt.Errorf("balancemonitor: decode json: %w", err)
	}

	var balanceUSD float64
	switch {
	case br.Balance != nil:
		balanceUSD = *br.Balance
	case br.TotalAvailable != nil:
		balanceUSD = *br.TotalAvailable
	default:
		return Reading{}, ErrNoBalance
	}

	now := m.nowFn()

	// MUTATION-KILL GUARD — TestLowBalanceWarns pins this comparison.
	// Changing < to <= or > (or removing the condition) makes that test fail.
	low := balanceUSD < m.floorUSD

	r := Reading{
		BalanceUSD: balanceUSD,
		LowBalance: low,
		RunID:      runID,
		At:         now,
	}

	m.mu.Lock()
	m.events = append(m.events, Event{
		Kind:    "reading",
		RunID:   runID,
		Balance: balanceUSD,
		At:      now,
	})
	if low {
		m.events = append(m.events, Event{
			Kind:    "low_balance_warning",
			RunID:   runID,
			Balance: balanceUSD,
			At:      now,
		})
	}
	m.mu.Unlock()

	return r, nil
}

// Events returns a snapshot of every event recorded so far.  The slice is a
// copy; the caller may modify it freely.
func (m *Monitor) Events() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Event, len(m.events))
	copy(out, m.events)
	return out
}
