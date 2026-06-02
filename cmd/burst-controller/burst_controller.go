// Package main implements a utilization-driven burst-controller binary that
// drives the bursthysteresis.BurstController and emits structured log lines
// for every SPILL and RECOVER transition.
//
// Testable core: Run(runID, samples, out) is the unit under test.
// main() wires a real sample source (--samples flag or stdin) to Run.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/HelixDevelopment/helix_cluster/pkg/bursthysteresis"
)

// ErrNoSamples is returned by Run when the samples slice is empty.
var ErrNoSamples = errors.New("burst-controller: no samples provided")

// Run feeds every sample in order into a BurstController built from
// bursthysteresis.DefaultConfig(runID).
//
// On each SPILL transition it writes a line of the form:
//
//	SPILL runID=<runID> sample=<v> burst allocation request
//
// On each RECOVER transition it writes:
//
//	RECOVER runID=<runID> sample=<v>
//
// MUTATION GUARD (pinned by TestRunEmitsBurstRequestOnSpill):
// The Fprintf call below that emits "burst allocation request" is the sole
// line that writes that substring to out. Removing or blanking it causes
// TestRunEmitsBurstRequestOnSpill to fail because the test asserts that the
// captured output contains exactly one line with the substring.
func Run(runID string, samples []float64, out io.Writer) error {
	if len(samples) == 0 {
		return ErrNoSamples
	}

	cfg := bursthysteresis.DefaultConfig(runID)
	ctrl, err := bursthysteresis.New(cfg)
	if err != nil {
		return fmt.Errorf("burst-controller: failed to create controller: %w", err)
	}

	// Register a subscriber that writes to out for every transition event.
	ctrl.Subscribe(func(ev bursthysteresis.Event) {
		switch ev.Kind {
		case bursthysteresis.EventSpill:
			// MUTATION GUARD — pinned by TestRunEmitsBurstRequestOnSpill:
			// This line is the sole emitter of "burst allocation request".
			// Deleting or emptying it makes TestRunEmitsBurstRequestOnSpill fail
			// because the test scans the output for that exact substring.
			fmt.Fprintf(out, "SPILL runID=%s sample=%.4f burst allocation request\n", ev.RunID, ev.Sample)
		case bursthysteresis.EventRecover:
			fmt.Fprintf(out, "RECOVER runID=%s sample=%.4f\n", ev.RunID, ev.Sample)
		}
	})

	ctrl.FeedAll(samples)
	return nil
}

// readSamplesFromReader parses whitespace/newline-separated float64 values
// from r, returning the slice or an error on the first unparseable token.
func readSamplesFromReader(r io.Reader) ([]float64, error) {
	var samples []float64
	scanner := bufio.NewScanner(r)
	scanner.Split(bufio.ScanWords)
	for scanner.Scan() {
		tok := strings.TrimSpace(scanner.Text())
		if tok == "" {
			continue
		}
		v, err := strconv.ParseFloat(tok, 64)
		if err != nil {
			return nil, fmt.Errorf("burst-controller: cannot parse %q as float64: %w", tok, err)
		}
		samples = append(samples, v)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("burst-controller: scanner error: %w", err)
	}
	return samples, nil
}

func main() {
	runID := flag.String("run-id", "burst-controller-run", "unique identifier for this run, embedded in every output line")
	samplesFlag := flag.String("samples", "", "comma-separated utilization samples in [0,1] (e.g. 0.50,0.95,0.80,0.62); if empty, samples are read from stdin")
	flag.Parse()

	var samples []float64
	var err error

	if *samplesFlag != "" {
		parts := strings.Split(*samplesFlag, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			v, parseErr := strconv.ParseFloat(p, 64)
			if parseErr != nil {
				fmt.Fprintf(os.Stderr, "burst-controller: invalid sample %q: %v\n", p, parseErr)
				os.Exit(1)
			}
			samples = append(samples, v)
		}
	} else {
		samples, err = readSamplesFromReader(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	}

	if err := Run(*runID, samples, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}
