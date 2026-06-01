package covgate

import (
	"bytes"
	"testing"
)

// FuzzParseCoverProfile drives ParseCoverProfile with arbitrary byte sequences.
//
// Properties asserted on every successful parse (err == nil):
//   - CoveredStatements <= TotalStatements  (a broken counter could violate this)
//   - Aggregate in [0.0, 100.0]             (a broken percentage formula could go negative or >100)
//   - Mode is non-empty                     (the parser must always populate Mode on success)
//
// On any input the function MUST NOT panic; if it cannot parse the input it
// must return a non-nil error. A panic on a fuzzed seed corpus entry is itself
// a test failure.
//
// Seeds cover the main happy-path variants as well as adversarial truncations
// and malformations so that the seed-corpus run (plain `go test`, no -fuzz)
// exercises all assertion branches.
func FuzzParseCoverProfile(f *testing.F) {
	// Seed 1: minimal valid profile — mode line only, no blocks.
	f.Add([]byte("mode: set\n"))

	// Seed 2: valid profile with a single covered block.
	f.Add([]byte("mode: count\ngithub.com/foo/bar/pkg/x/file.go:1.10,5.3 3 1\n"))

	// Seed 3: valid profile with multiple blocks and a zero-count (uncovered) block.
	f.Add([]byte(
		"mode: atomic\n" +
			"github.com/foo/bar/a.go:1.1,2.2 2 5\n" +
			"github.com/foo/bar/a.go:3.1,4.2 1 0\n" +
			"github.com/foo/bar/b.go:10.5,20.1 8 3\n",
	))

	// Seed 4: malformed — missing mode prefix.
	f.Add([]byte("set\ngithub.com/foo/bar/a.go:1.1,2.2 1 1\n"))

	// Seed 5: malformed — mode line present but block has wrong field count.
	f.Add([]byte("mode: set\ngithub.com/x.go:1.1,2.2\n"))

	// Seed 6: empty input.
	f.Add([]byte{})

	// Seed 7: truncated mid-block line.
	f.Add([]byte("mode: set\ngithub.com/foo/bar/a.go:1.1,2.2 2"))

	// Seed 8: mode line with trailing whitespace.
	f.Add([]byte("mode:  atomic  \ngithub.com/foo/bar/a.go:1.1,2.2 1 1\n"))

	// Seed 9: negative numStmts.
	f.Add([]byte("mode: set\ngithub.com/foo/bar/a.go:1.1,2.2 -1 1\n"))

	// Seed 10: block spec missing ':' separator.
	f.Add([]byte("mode: set\ngithub.com/foo/bar/a.go 1 1\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		// MUST NOT panic under any input.
		report, err := ParseCoverProfile(bytes.NewReader(data))
		if err != nil {
			// Any error is acceptable; the only requirement is no panic.
			return
		}

		// Property 1: CoveredStatements <= TotalStatements.
		// A broken counter accumulation (e.g. swapped increment branches) would
		// make covered > total, causing this assertion to fire.
		if report.CoveredStatements > report.TotalStatements {
			t.Fatalf("invariant violated: CoveredStatements=%d > TotalStatements=%d",
				report.CoveredStatements, report.TotalStatements)
		}

		// Property 2: Aggregate in [0.0, 100.0].
		// A broken percentage formula could go negative (covered/total with sign
		// error) or exceed 100 (counter overflow / off-by-one).
		if report.Aggregate < 0.0 || report.Aggregate > 100.0 {
			t.Fatalf("invariant violated: Aggregate=%f not in [0, 100]", report.Aggregate)
		}

		// Property 3: Mode must not be empty on a successful parse.
		// If the parser were to accept an empty mode and return success, this
		// gate fires.
		if report.Mode == "" {
			t.Fatalf("invariant violated: Mode is empty on a successful parse")
		}
	})
}
