// Package covgate provides coverage-profile parsing and mutation-pairing audit
// tools that back the HXC-1138 Constitution §1.1 exit gate.
//
// Usage summary:
//
//	// Parse a coverprofile written by 'go test -coverprofile=cover.out':
//	f, _ := os.Open("cover.out")
//	report, err := covgate.ParseCoverProfile(f)
//
//	// Check aggregate threshold:
//	ok := report.MeetsThreshold(80.0)
//
//	// Find packages below threshold:
//	bad := report.Shortfalls("github.com/HelixDevelopment/helix_cluster/pkg", 80.0)
//
//	// Audit mutation pairing:
//	catalogFile, _ := os.Open("test/mutation/run_mutations.sh")
//	cases, _ := covgate.ParseMutationCatalog(catalogFile)
//	missing := covgate.MissingMutationPairs(testedPkgs, cases)
package covgate
