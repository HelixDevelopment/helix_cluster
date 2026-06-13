module github.com/HelixDevelopment/helix_cluster/hwinventory

go 1.24

require (
	github.com/HelixDevelopment/helix_cluster/devicedetect v0.0.0
	github.com/HelixDevelopment/helix_cluster/fpgadetect v0.0.0
	github.com/HelixDevelopment/helix_cluster/npudetect v0.0.0
)

// For standalone (GOWORK=off) builds these resolve to the sibling modules in
// the same repo. The root go.work resolves them in-workspace and ignores these
// replace directives.
replace github.com/HelixDevelopment/helix_cluster/devicedetect => ../devicedetect

replace github.com/HelixDevelopment/helix_cluster/fpgadetect => ../fpgadetect

replace github.com/HelixDevelopment/helix_cluster/npudetect => ../npudetect
