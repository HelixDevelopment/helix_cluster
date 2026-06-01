package runner

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Suite is a test suite function that runs work under ctx and returns its Result.
type Suite func(ctx context.Context) Result

// Options configures TestRunner behaviour.
type Options struct {
	// MaxConcurrency limits how many suites execute simultaneously.
	// Values <= 0 default to 4.
	MaxConcurrency int
}

// TestRunner registers and runs named Suites with bounded parallelism.
type TestRunner struct {
	opts   Options
	mu     sync.Mutex
	suites []namedSuite
	names  map[string]struct{}
}

type namedSuite struct {
	name  string
	suite Suite
}

var (
	// ErrDuplicateName is returned by Register when the name is already registered.
	ErrDuplicateName = errors.New("runner: suite name already registered")
	// ErrEmptyName is returned by Register when an empty name is provided.
	ErrEmptyName = errors.New("runner: suite name must not be empty")
)

// New creates a TestRunner with the given Options.
// MaxConcurrency defaults to 4 when opts.MaxConcurrency <= 0.
func New(opts Options) *TestRunner {
	if opts.MaxConcurrency <= 0 {
		opts.MaxConcurrency = 4
	}
	return &TestRunner{
		opts:  opts,
		names: make(map[string]struct{}),
	}
}

// Register adds a named Suite to the runner.
// Returns ErrEmptyName if name is empty, ErrDuplicateName if name is already taken.
func (r *TestRunner) Register(name string, s Suite) error {
	if name == "" {
		return ErrEmptyName
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.names[name]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateName, name)
	}
	r.names[name] = struct{}{}
	r.suites = append(r.suites, namedSuite{name: name, suite: s})
	return nil
}

// Run executes all registered suites with bounded concurrency and returns an
// aggregated Report. Suites in the Report are sorted by Name for determinism.
//
// If ctx is cancelled before a suite is dispatched it will not be started;
// suites already running continue to completion.
func (r *TestRunner) Run(ctx context.Context) Report {
	r.mu.Lock()
	suites := make([]namedSuite, len(r.suites))
	copy(suites, r.suites)
	r.mu.Unlock()

	type workItem struct {
		ns namedSuite
	}

	results := make([]SuiteResult, 0, len(suites))
	var resultsMu sync.Mutex

	// semaphore channel limits concurrency to MaxConcurrency
	sem := make(chan struct{}, r.opts.MaxConcurrency)

	var wg sync.WaitGroup

	for i := range suites {
		ns := suites[i]

		// Check ctx before dispatching the next suite.
		select {
		case <-ctx.Done():
			// context cancelled — stop dispatching new suites
			goto done
		default:
		}

		// Acquire a concurrency slot (blocks if at cap, but still watches ctx).
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			goto done
		}

		wg.Add(1)
		go func(ns namedSuite) {
			defer wg.Done()
			defer func() { <-sem }()

			start := time.Now()
			res := ns.suite(ctx)
			dur := time.Since(start)

			sr := SuiteResult{
				Name:     ns.name,
				Passed:   res.Passed && res.Err == nil,
				Duration: dur,
				Err:      res.Err,
			}
			// A suite returning Err != nil is always Failed regardless of Passed flag.
			if res.Err != nil {
				sr.Passed = false
			}

			resultsMu.Lock()
			results = append(results, sr)
			resultsMu.Unlock()
		}(ns)
	}

done:
	wg.Wait()

	// Sort deterministically by Name.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Name < results[j].Name
	})

	var maxDur time.Duration
	passed, failed := 0, 0
	for _, sr := range results {
		if sr.Passed {
			passed++
		} else {
			failed++
		}
		if sr.Duration > maxDur {
			maxDur = sr.Duration
		}
	}

	return Report{
		Total:       len(results),
		Passed:      passed,
		Failed:      failed,
		Suites:      results,
		MaxDuration: maxDur,
	}
}
