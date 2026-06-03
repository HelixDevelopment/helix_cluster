package gitops

import "sort"

// Resource identifies a single Kubernetes resource managed by an Application,
// using the (Group, Kind, Namespace, Name) tuple ArgoCD uses to track managed
// resources. Two resources are equal iff all four fields match.
type Resource struct {
	Group     string `json:"group" yaml:"group"`
	Kind      string `json:"kind" yaml:"kind"`
	Namespace string `json:"namespace" yaml:"namespace"`
	Name      string `json:"name" yaml:"name"`
}

// key is the comparable map key for a Resource.
func (r Resource) key() Resource { return r }

// DriftReport is the outcome of comparing the Git-desired manifest set against
// the live reported state of a cell. ToPrune holds resources present LIVE but
// absent from DESIRED — i.e. removed from Git and therefore to be pruned by an
// automated prune+selfHeal sync. Missing holds resources DESIRED but not yet
// live (need apply). InSync is true when both are empty.
type DriftReport struct {
	ToPrune []Resource
	Missing []Resource
}

// InSync reports whether the live state matches the desired state exactly.
func (d DriftReport) InSync() bool { return len(d.ToPrune) == 0 && len(d.Missing) == 0 }

// DetectDrift compares the desired resource set (from Git) against the live
// resource set (reported by ArgoCD for a cell) and returns a DriftReport.
//
//   - A resource in live but not in desired is reported in ToPrune: it was
//     removed from Git and an automated prune+selfHeal sync must delete it from
//     the cell. This is the load-bearing branch for the closure criterion's
//     "removed Git resource pruned from a cell (self-heal)".
//   - A resource in desired but not in live is reported in Missing.
//
// Both result slices are sorted (by Group,Kind,Namespace,Name) for deterministic
// output. The function is pure: it inspects sets, it does not call any API.
func DetectDrift(desired, live []Resource) DriftReport {
	desiredSet := make(map[Resource]struct{}, len(desired))
	for _, r := range desired {
		desiredSet[r.key()] = struct{}{}
	}
	liveSet := make(map[Resource]struct{}, len(live))
	for _, r := range live {
		liveSet[r.key()] = struct{}{}
	}

	var rep DriftReport
	for r := range liveSet {
		if _, ok := desiredSet[r]; !ok {
			rep.ToPrune = append(rep.ToPrune, r)
		}
	}
	for r := range desiredSet {
		if _, ok := liveSet[r]; !ok {
			rep.Missing = append(rep.Missing, r)
		}
	}
	sortResources(rep.ToPrune)
	sortResources(rep.Missing)
	return rep
}

func sortResources(rs []Resource) {
	sort.Slice(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
}
