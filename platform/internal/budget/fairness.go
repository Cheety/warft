package budget

import "sort"

// Fairness is SP-V04-4: weighted shares of the bottleneck, not a queue per tenant. The scarcest
// resource changes (R-C), so what is shared out here is a number of units of *whatever* is scarce
// at this moment — pod minutes today, a token bucket tomorrow — and this file knows nothing about
// which.
//
// The rule is weighted max-min fairness, filled progressively: every claimant gets its weighted
// share of the bottleneck; whoever wants less than its share takes only what it wants, and what is
// left over is shared out again among those who are still short. Two properties follow, and they are
// the two AB-V04-4 measures:
//
//   - a heavy sender gets a lot, not everything — it is bounded by its weighted share while anyone
//     else is short
//   - nobody is starved — a light sender's small claim is always met in full while capacity remains,
//     no matter how much noise the heavy one makes
//
// A queue per tenant would give the same answer only while every tenant is busy; the moment one goes
// quiet its queue idles instead of being shared out, which is why V-04 does not ask for one.

// Claim is what one principal wants of the bottleneck, and the weight it carries. A weight of zero
// or less is read as one: a claimant that carries no weight would never be served, and a share of
// nothing is not a fairness rule but a ban.
type Claim struct {
	Principal string
	Weight    int64
	Want      int64
}

// Grant is what the claim gets.
type Grant struct {
	Principal string
	Weight    int64
	Want      int64
	Granted   int64
}

// Short reports whether this claimant is still asking for more than it was given.
func (g Grant) Short() bool { return g.Granted < g.Want }

// Share hands out `capacity` units of the bottleneck across the claims, weighted max-min fair. The
// result is sorted by principal so two runs over the same claims produce the same grants — a
// fairness rule whose answer depends on map order is not one.
func Share(capacity int64, claims []Claim) []Grant {
	grants := make([]Grant, 0, len(claims))
	for _, c := range claims {
		w := c.Weight
		if w < 1 {
			w = 1
		}
		want := c.Want
		if want < 0 {
			want = 0
		}
		grants = append(grants, Grant{Principal: c.Principal, Weight: w, Want: want})
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].Principal < grants[j].Principal })

	left := capacity
	for left > 0 {
		var totalWeight int64
		for _, g := range grants {
			if g.Short() {
				totalWeight += g.Weight
			}
		}
		if totalWeight == 0 {
			break
		}

		var handedOut int64
		for i := range grants {
			g := &grants[i]
			if !g.Short() {
				continue
			}
			// The share is computed against the capacity this round started with, so a claimant
			// served early in the loop does not eat the share of one served later.
			share := (left * g.Weight) / totalWeight
			take := min64(share, g.Want-g.Granted)
			g.Granted += take
			handedOut += take
		}
		left -= handedOut

		if handedOut == 0 {
			// Fewer units left than there are claimants: the remainder goes by weight, and ties by
			// principal, one unit at a time. Rounding down would otherwise leave the last few units
			// of the bottleneck unspent for ever.
			order := make([]int, 0, len(grants))
			for i := range grants {
				if grants[i].Short() {
					order = append(order, i)
				}
			}
			sort.SliceStable(order, func(a, b int) bool {
				return grants[order[a]].Weight > grants[order[b]].Weight
			})
			for _, i := range order {
				if left == 0 {
					break
				}
				grants[i].Granted++
				left--
			}
			break
		}
	}
	return grants
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
