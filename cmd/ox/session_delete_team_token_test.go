package main

import (
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Under a team token, abort, delete and remove each refuse a session that is in
// the Ledger and link its web page, where a person deletes it (#1130). Under a
// personal token the same command still deletes it from the remote.
func TestLedgerSessionDelete_RefusedUnderTeamToken(t *testing.T) {
	const sessionName = "2026-01-01T00-00-testuser-OxTeam1"
	inst := &agentinstance.Instance{AgentID: "OxTeam1"}

	commands := []struct {
		name string
		run  func(t *testing.T) error
	}{
		{"abort", func(t *testing.T) error {
			setForceFlag(t, true)
			return runAgentSessionAbort(inst, agentCmd, []string{sessionName})
		}},
		{"delete", func(t *testing.T) error {
			setForceFlag(t, true)
			return runAgentSessionDelete(inst, agentCmd, []string{sessionName})
		}},
		{"remove", func(t *testing.T) error {
			store, err := session.NewStore(t.TempDir())
			require.NoError(t, err)
			return removeSessionByPattern(store, sessionName, true)
		}},
	}
	tokens := []struct {
		name    string
		value   string
		refused bool
	}{
		{"team token", "oxt_test_1ljPfr", true},
		{"personal token", "oxp_test_4bDZfN", false},
	}

	for _, c := range commands {
		for _, tok := range tokens {
			t.Run(c.name+"/"+tok.name, func(t *testing.T) {
				setTestCfg(t)
				f := newDraftLedgerFixture(t)
				t.Chdir(f.projectRoot)
				seedFinalizedLedgerSessionWithArtifacts(t, f.ledgerPath, sessionName)
				commitAndPushFinalized(t, f, sessionName)
				t.Setenv("SAGEOX_TOKEN", tok.value)

				var err error
				captureStdoutForPlanCLI(t, func() { err = c.run(t) })

				metaPath := "sessions/" + sessionName + "/meta.json"
				if !tok.refused {
					require.NoError(t, err)
					assert.NotContains(t, remoteTree(t, f.barePath), metaPath)
					return
				}
				assert.Contains(t, remoteTree(t, f.barePath), metaPath, "a team token must not delete a Ledger session")
				assert.FileExists(t, filepath.Join(f.ledgerPath, filepath.FromSlash(metaPath)))
				require.Error(t, err)
				assert.Contains(t, err.Error(), "Delete it on the web: ")
				assert.Contains(t, err.Error(), "/c/"+sessionScopedID(sessionName))
			})
		}
	}
}
