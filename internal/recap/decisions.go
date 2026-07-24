package recap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/sageox/ox/pkg/sessionsummary"
)

// gatherDecisions reads the summary.json of each of the user's sessions and
// collects the decisions their agent recorded — the already-settled calls the
// team now inherits and does not re-litigate. Newest sessions first so the most
// current decisions lead; capped. Fail-open per session.
func gatherDecisions(ledgerPath string, sessions []SessionFacts) []Decision {
	// newest first
	ordered := make([]SessionFacts, len(sessions))
	copy(ordered, sessions)
	sortByCreatedDesc(ordered)

	var out []Decision
	for _, s := range ordered {
		if len(out) >= maxDecisions {
			break
		}
		for _, d := range readSessionDecisions(ledgerPath, s) {
			if d.What == "" {
				continue
			}
			out = append(out, Decision{
				What:    truncate(d.What, snippetMax),
				Why:     truncate(d.Why, snippetMax),
				Owner:   d.Owner,
				Session: s.displayTitle(),
				Receipt: "session:" + s.Name,
			})
			if len(out) >= maxDecisions {
				break
			}
		}
	}
	return out
}

// readSessionDecisions loads a session's summary.json and returns its recorded
// decisions. summary.json is a plain (non-LFS) artifact; the in-place copy is
// authoritative, with the ledger cache as fallback.
func readSessionDecisions(ledgerPath string, s SessionFacts) []sessionsummary.Decision {
	for _, p := range []string{
		filepath.Join(s.Dir, "summary.json"),
		filepath.Join(ledgerPath, ".sageox", "cache", "sessions", s.Name, "summary.json"),
	} {
		data, err := os.ReadFile(p)
		if err != nil || len(data) == 0 {
			continue
		}
		var resp sessionsummary.SummarizeResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			continue
		}
		if resp.AgentSummary == nil {
			return nil
		}
		return resp.AgentSummary.Decisions
	}
	return nil
}

// sortByCreatedDesc orders sessions newest-first, in place.
func sortByCreatedDesc(facts []SessionFacts) {
	sort.Slice(facts, func(i, j int) bool {
		return facts[i].CreatedAt.After(facts[j].CreatedAt)
	})
}
