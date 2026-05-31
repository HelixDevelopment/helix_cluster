package marketplace

import (
	"math"
	"testing"
)

// ids extracts the ranked offer IDs in order, for exact-ordering assertions.
func ids(scored []ScoredOffer) []string {
	out := make([]string, len(scored))
	for i, s := range scored {
		out[i] = s.Offer.ID
	}
	return out
}

func eqIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const eps = 1e-9

// cheapSlow vs priceyFast: A is cheaper but slower, B is pricier but faster.
// Both share availability so that dimension is neutral. Hand computation under
// DefaultWeights (price .3, avail .3, latency .2, throughput .2):
//
//	price:  min 1, max 3  -> A=(3-1)/2=1.0  B=0.0
//	avail:  90==90        -> A=1.0          B=1.0   (non-discriminating)
//	latency:min 50,max 200-> A=(200-200)/150=0.0  B=(200-50)/150=1.0
//	thr:    min 100,max400-> A=0.0          B=(400-100)/300=1.0
//	A = .3*1 + .3*1 + .2*0 + .2*0 = 0.6
//	B = .3*0 + .3*1 + .2*1 + .2*1 = 0.7
//
// So B ("pricey-fast") ranks first with a pinned score of exactly 0.7.
var (
	cheapSlow  = Offer{ID: "cheap-slow", PricePerHour: 1.0, AvailabilityPct: 90, LatencyMS: 200, ThroughputTokS: 100}
	priceyFast = Offer{ID: "pricey-fast", PricePerHour: 3.0, AvailabilityPct: 90, LatencyMS: 50, ThroughputTokS: 400}
)

// TestRank_WeightedOrdering_PinnedTopScore proves the scorer ranks per the
// weights and a hand-computed expected order, with a PINNED top score.
//
// Mutation that this kills: invert a "lower is better" dimension, e.g. in
// normLowerBetter return (v-min)/(max-min) instead of (max-v)/(max-min). Then
// the cheaper/faster mapping flips: priceyFast's composite drops to 0.3 and
// cheapSlow rises to 0.4, so cheapSlow ranks first and the pinned 0.7 top score
// no longer holds — both the order assertion and the score assertion FAIL.
func TestRank_WeightedOrdering_PinnedTopScore(t *testing.T) {
	got := CompositeScorer{}.Rank([]Offer{cheapSlow, priceyFast}, DefaultWeights, false)

	want := []string{"pricey-fast", "cheap-slow"}
	if !eqIDs(ids(got), want) {
		t.Fatalf("ranking = %v, want %v", ids(got), want)
	}
	if math.Abs(got[0].Score-0.7) > eps {
		t.Fatalf("top score = %v, want exactly 0.7", got[0].Score)
	}
	if math.Abs(got[1].Score-0.6) > eps {
		t.Fatalf("second score = %v, want exactly 0.6", got[1].Score)
	}
}

// TestRank_WeightZeroRemovesInfluence proves a dimension with weight 0 carries
// no influence: making price the ONLY weighted dimension flips the winner to
// the cheaper offer.
//
// Under Weights{Price:1} (weightSum=1, other terms multiplied by 0 weight):
//
//	A(cheap) price norm = 1.0 -> score 1.0
//	B(pricey) price norm = 0.0 -> score 0.0
//
// so cheap-slow now ranks first — the exact opposite of the default-weight
// ordering above.
//
// Mutation that this kills: ignore the weights and average all four normalised
// dimensions equally (composite = (nPrice+nAvail+nLat+nThr)/4). Then the
// price-only weighting has no effect: cheap-slow's pinned price-only score of
// 1.0 collapses to (1+1+0+0)/4 = 0.5, so the "== 1.0" score assertion FAILS.
func TestRank_WeightZeroRemovesInfluence(t *testing.T) {
	priceOnly := Weights{Price: 1.0}
	got := CompositeScorer{}.Rank([]Offer{cheapSlow, priceyFast}, priceOnly, false)

	want := []string{"cheap-slow", "pricey-fast"}
	if !eqIDs(ids(got), want) {
		t.Fatalf("price-only ranking = %v, want %v", ids(got), want)
	}
	if math.Abs(got[0].Score-1.0) > eps {
		t.Fatalf("cheap-slow price-only score = %v, want exactly 1.0", got[0].Score)
	}
	if math.Abs(got[1].Score-0.0) > eps {
		t.Fatalf("pricey-fast price-only score = %v, want exactly 0.0", got[1].Score)
	}
}

// TestRank_RequireTEE_PromotesConfidentialOffer is the anti-bluff TEE test.
//
// Setup: a NON-confidential offer that would otherwise win, and a confidential
// offer that would otherwise lose. With requireTEE=false the strong
// non-confidential offer wins. Flipping requireTEE=true applies the 1.5x
// multiplier ONLY to the confidential offer, lifting it to the top.
//
// Three offers so dimensions normalise to fractional (not just 0/1) values; a
// "filler" pins the min/max range. DefaultWeights (.3/.3/.2/.2):
//
//	ranges: price[1,3] avail[85,100] lat[60,100] thr[300,360]
//	                price      avail        lat          thr
//	winner-plain    1 ->1.0    100 ->1.0    90 ->0.25    330 ->0.5
//	tee-mid         2.5->0.25  90  ->0.333  60 ->1.0     360 ->1.0
//	filler          3 ->0.0    85  ->0.0    100->0.0     300 ->0.0
//	plain  = .3*1.0  + .3*1.0   + .2*0.25 + .2*0.5  = 0.750
//	tee    = .3*0.25 + .3*0.333 + .2*1.0  + .2*1.0  = 0.575
//	filler = 0.0
//	requireTEE=false -> winner-plain (0.750) first, tee-mid (0.575) second.
//	requireTEE=true  -> tee-mid * 1.5 = 0.8625 > 0.750 -> tee-mid first.
//
// We assert: requireTEE=false -> winner-plain first; requireTEE=true ->
// tee-mid first AND its ScoredOffer.TEEBoosted is true.
//
// Mutation that this kills: a scorer that ignores the TEE multiplier (delete the
// `if requireTEE && o.Confidential { composite *= TEEMultiplier }` block, or set
// TEEMultiplier to 1.0). Then requireTEE=true leaves the ranking unchanged
// (winner-plain 0.750 still beats tee-mid 0.575), winner-plain stays on top,
// and the "tee-mid rises to the top" assertion FAILS.
func TestRank_RequireTEE_PromotesConfidentialOffer(t *testing.T) {
	winnerPlain := Offer{ID: "winner-plain", PricePerHour: 1, AvailabilityPct: 100, LatencyMS: 90, ThroughputTokS: 330, Confidential: false}
	teeMid := Offer{ID: "tee-mid", PricePerHour: 2.5, AvailabilityPct: 90, LatencyMS: 60, ThroughputTokS: 360, Confidential: true}
	filler := Offer{ID: "filler", PricePerHour: 3, AvailabilityPct: 85, LatencyMS: 100, ThroughputTokS: 300, Confidential: false}
	offers := []Offer{winnerPlain, teeMid, filler}

	// Without TEE requirement the strong non-confidential offer wins.
	noTEE := CompositeScorer{}.Rank(offers, DefaultWeights, false)
	if noTEE[0].Offer.ID != "winner-plain" {
		t.Fatalf("requireTEE=false winner = %q, want winner-plain", noTEE[0].Offer.ID)
	}
	if noTEE[0].TEEBoosted {
		t.Fatalf("requireTEE=false must not boost any offer, but top was boosted")
	}
	// Pin the hand-computed plain scores.
	if math.Abs(noTEE[0].Score-0.750) > eps {
		t.Fatalf("winner-plain plain score = %v, want exactly 0.750", noTEE[0].Score)
	}
	if math.Abs(noTEE[1].Score-0.575) > eps {
		t.Fatalf("tee-mid plain score = %v, want exactly 0.575", noTEE[1].Score)
	}

	// Flip requireTEE: the confidential offer must rise to the top via the 1.5x.
	withTEE := CompositeScorer{}.Rank(offers, DefaultWeights, true)
	if withTEE[0].Offer.ID != "tee-mid" {
		t.Fatalf("requireTEE=true winner = %q, want tee-mid (TEE promotion failed)", withTEE[0].Offer.ID)
	}
	if !withTEE[0].TEEBoosted {
		t.Fatalf("requireTEE=true top offer must be marked TEEBoosted")
	}

	// And the boost must be exactly the multiplier applied to its plain score.
	plainTEE := noTEE[1] // tee-mid's unboosted score from the first ranking
	if plainTEE.Offer.ID != "tee-mid" {
		t.Fatalf("setup: expected tee-mid as runner-up in no-TEE ranking, got %q", plainTEE.Offer.ID)
	}
	if math.Abs(withTEE[0].Score-plainTEE.Score*TEEMultiplier) > eps {
		t.Fatalf("boosted score = %v, want plain %v * %v", withTEE[0].Score, plainTEE.Score, TEEMultiplier)
	}
}

// TestRank_SingleOffer proves normalisation handles a single offer: every
// dimension is non-discriminating (max==min) so it scores a perfect 1.0
// composite under any non-zero weights.
//
// Mutation that this kills: in normHigherBetter/normLowerBetter, on the
// max==min branch return 0 instead of 1. Then the lone offer scores 0.0 and the
// "== 1.0" assertion FAILS.
func TestRank_SingleOffer(t *testing.T) {
	got := CompositeScorer{}.Rank([]Offer{cheapSlow}, DefaultWeights, false)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if math.Abs(got[0].Score-1.0) > eps {
		t.Fatalf("single-offer score = %v, want exactly 1.0", got[0].Score)
	}
}

// TestRank_IdenticalOffers proves identical offers all score identically (1.0)
// and the ordering is a deterministic ID tie-break, not arbitrary.
//
// Mutation that this kills: replace sort.SliceStable's tie-break
// `out[a].Offer.ID < out[b].Offer.ID` with `> ` (reverse). Then the tie order
// becomes b,a and the {"a","b"} assertion FAILS. Also catches the max==min
// branch returning anything but 1.0 (scores would not all be 1.0).
func TestRank_IdenticalOffers(t *testing.T) {
	base := Offer{PricePerHour: 2, AvailabilityPct: 95, LatencyMS: 100, ThroughputTokS: 300}
	a := base
	a.ID = "a"
	b := base
	b.ID = "b"
	got := CompositeScorer{}.Rank([]Offer{b, a}, DefaultWeights, false)

	if !eqIDs(ids(got), []string{"a", "b"}) {
		t.Fatalf("identical-offer tie order = %v, want [a b]", ids(got))
	}
	for _, s := range got {
		if math.Abs(s.Score-1.0) > eps {
			t.Fatalf("identical offer %q score = %v, want exactly 1.0", s.Offer.ID, s.Score)
		}
	}
}

// TestRank_Empty returns nil for no offers.
//
// Mutation that this kills: drop the `if n == 0 { return nil }` guard; Rank then
// indexes offers[0] and panics, failing this test.
func TestRank_Empty(t *testing.T) {
	got := CompositeScorer{}.Rank(nil, DefaultWeights, false)
	if got != nil {
		t.Fatalf("empty Rank = %v, want nil", got)
	}
}
