package recap

import (
	"time"

	"github.com/sageox/ox/internal/plan"
)

// gatherPlans mines the plans SageOx enriched for the user — the collisions,
// prior-art matches, and expert routes it caught before code, drawn from the
// user's OWN history. This is core solo value: even a team of one gets warned
// off their own open PRs and pointed at their own prior work. Window-scoped,
// author-filtered, fail-open.
func gatherPlans(projectRoot string, since, until time.Time, id Identity) []PlanEnriched {
	infos, err := plan.List(projectRoot)
	if err != nil {
		return nil
	}

	var out []PlanEnriched
	for _, info := range infos {
		if len(out) >= maxPlans {
			break
		}
		if !inWindow(info.CreatedAt, since, until) {
			continue
		}
		if !planIsMine(id, info) {
			continue
		}
		_, result, _, err := plan.Load(projectRoot, info.Slug)
		if err != nil {
			continue
		}
		s := result.Signals
		if s.Collisions == 0 && s.PriorArt == 0 && s.ExpertRoutes == 0 {
			continue // nothing was caught — not a value receipt
		}
		out = append(out, PlanEnriched{
			Slug:         info.Slug,
			Topic:        info.Topic,
			When:         info.CreatedAt,
			Collisions:   s.Collisions,
			PriorArt:     s.PriorArt,
			ExpertRoutes: s.ExpertRoutes,
			Receipt:      info.Dir,
		})
	}
	return out
}

// planIsMine reports whether a plan belongs to the identity. A plan authored by
// this user (by display name) is theirs; when identity is unresolved (e.g.
// unauthenticated) every plan is included, since a solo player's plans are all
// their own.
func planIsMine(id Identity, info plan.PlanInfo) bool {
	if id.DisplayName == "" && id.UserID == "" {
		return true
	}
	for _, a := range info.Authors {
		if equalFold(a, id.DisplayName) {
			return true
		}
	}
	return len(info.Authors) == 0
}
