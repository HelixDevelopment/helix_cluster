# Software Bill of Materials (SBOM)

HelixCluster publishes a [CycloneDX](https://cyclonedx.org/) SBOM for each of its
tracked Go modules. SBOMs enumerate every direct and transitive dependency so the
project's supply chain can be audited (vulnerability scanning, license review,
provenance). Generation is wired into the build via `make sbom` (HXC-1635).

## Generate

Install the generator once (host tool, lands in `$(go env GOPATH)/bin`, usually
`~/go/bin`):

```sh
go install github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@latest
export PATH="$(go env GOPATH)/bin:$PATH"   # ensure it is on PATH
```

Then run:

```sh
make sbom
```

This invokes `scripts/gen-sbom.sh`, which writes one CycloneDX 1.6 JSON document
per module into `sbom/`:

| File | Module |
|------|--------|
| `sbom/helixcluster.cdx.json` | `github.com/HelixDevelopment/helix_cluster` (main) |
| `sbom/api-v1.cdx.json`       | `github.com/HelixDevelopment/helix_cluster/api/v1` |
| `sbom/security.cdx.json`     | `digital.vasic.security` |

## Why per-module (workspace-off)

The repository uses a `go.work` workspace in which every `use` module is reported
as a main module. Under that workspace `cyclonedx-gomod` cannot disambiguate which
module is the SBOM subject and always selects the alphabetically-first one,
yielding three identical SBOMs with the wrong root component. `scripts/gen-sbom.sh`
therefore generates each SBOM with the workspace disabled (`GOWORK=off`) from inside
the module directory, producing a correct, distinct root component per module. The
script snapshots and restores each module's `go.mod`/`go.sum` so the working tree is
never left dirty, and temporarily moves any root-level `.DS_Store` aside (it trips
the tool's module-hash step).

## Tracked vs. generated

The `make sbom` target, `scripts/gen-sbom.sh`, and this note are the **tracked
deliverable**. The produced `sbom/*.cdx.json` files are build derivatives
(CONST-053) and are git-ignored — regenerate them with `make sbom` whenever the
dependency graph changes.

## Verify

Each emitted file is valid CycloneDX with a non-empty component list:

```sh
for f in sbom/*.cdx.json; do
  jq -e '.bomFormat == "CycloneDX" and (.components | length) > 0' "$f" \
    || { echo "INVALID/EMPTY SBOM: $f"; exit 1; }
done
```

A malformed or empty SBOM fails this check.
