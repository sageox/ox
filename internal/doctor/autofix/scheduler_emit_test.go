package autofix

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The sink must see Clean results. Clean is the only signal a previously
// reported issue is resolved, so filtering it out meant an issue raised once
// could never be retired for the daemon's whole lifetime.
//
// Failure prevented: "session meta titles: recovered=1" stayed pinned in
// `ox daemon status` for 19 hours after the titles it names were recovered,
// with no code path able to clear it — and `ox doctor`, which the accompanying
// hint told the user to run, has no way to clear daemon issues at all.
func TestScheduler_EmitsCleanSoIssuesCanBeRetired(t *testing.T) {
	reg := NewRegistry()
	status := StatusFound
	reg.Register(&Check{
		Slug: "flaky",
		Run: func(context.Context, string) CheckResult {
			return CheckResult{Status: status, Summary: "drift"}
		},
	})

	var got []CheckResult
	s := NewScheduler(reg, nil, func() []string { return []string{""} }, func(r CheckResult) {
		got = append(got, r)
	})

	s.RunNow(context.Background())
	require.Len(t, got, 1)
	assert.Equal(t, StatusFound, got[0].Status)

	// The check now reports healthy. The sink MUST hear about it.
	status = StatusClean
	got = nil
	s.RunNow(context.Background())
	require.Len(t, got, 1, "a Clean result must reach the sink, not be filtered out")
	assert.Equal(t, StatusClean, got[0].Status)
	assert.Equal(t, "flaky", got[0].Slug, "the sink needs the slug to know which issue to retire")
}
