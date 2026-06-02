// Package revenueopt implements HXC-1456: a RevenueOptimizer that performs a
// greedy GPU-to-marketplace assignment, maximizing expected hourly revenue per
// GPU. It is fully self-contained and depends only on the Go standard library.
package revenueopt

// GPU describes a single accelerator in the fleet.
//
// TEE=true means the GPU is trusted-execution-capable, which makes it eligible
// for the TEE->Chutes revenue boost (see RevenueOptimizer.TEEChutesBoost).
type GPU struct {
	ID    string
	Model string
	TEE   bool
}

// Offer is a marketplace bid for a particular GPU model.
type Offer struct {
	Marketplace       string
	GPUModel          string
	ExpectedHourlyUSD float64
}

// chutesMarketplace is the marketplace name that benefits from the TEE boost.
const chutesMarketplace = "Chutes"

// RevenueOptimizer performs greedy per-GPU marketplace selection.
//
// TEEChutesBoost is the fractional multiplier applied to expected revenue when a
// TEE-capable GPU is matched to the "Chutes" marketplace: the effective revenue
// becomes ExpectedHourlyUSD * (1 + TEEChutesBoost).
type RevenueOptimizer struct {
	TEEChutesBoost float64
}

// NewRevenueOptimizer constructs a RevenueOptimizer with the given boost factor.
func NewRevenueOptimizer(boost float64) *RevenueOptimizer {
	return &RevenueOptimizer{TEEChutesBoost: boost}
}

// effectiveRevenue returns the boost-adjusted expected revenue of matching the
// given GPU to the given offer. The TEE->Chutes boost is the load-bearing guard
// (HXC-1456): if it is removed/zeroed, a TEE GPU's Chutes revenue no longer wins
// against a slightly-richer competing marketplace.
func (o *RevenueOptimizer) effectiveRevenue(g GPU, off Offer) float64 {
	rev := off.ExpectedHourlyUSD
	if g.TEE && off.Marketplace == chutesMarketplace {
		rev *= (1 + o.TEEChutesBoost)
	}
	return rev
}

// OptimizeAllocation greedily assigns each GPU to the marketplace that maximizes
// its effective expected hourly revenue, considering only offers whose GPUModel
// matches the GPU's Model. The result maps gpuID -> chosen marketplace. GPUs with
// no matching offer are omitted from the result.
func (o *RevenueOptimizer) OptimizeAllocation(fleet []GPU, offers []Offer) map[string]string {
	result := make(map[string]string)
	for _, g := range fleet {
		bestMarketplace := ""
		bestRevenue := 0.0
		found := false
		for _, off := range offers {
			if off.GPUModel != g.Model {
				continue
			}
			rev := o.effectiveRevenue(g, off)
			if !found || rev > bestRevenue {
				found = true
				bestRevenue = rev
				bestMarketplace = off.Marketplace
			}
		}
		if found {
			result[g.ID] = bestMarketplace
		}
	}
	return result
}
