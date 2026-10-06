package daemon

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/storage/filesystem/dotgit"
	"github.com/stretchr/testify/assert"
)

func TestIsLedgerRepoChanged_GoGitShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "observed get child tree object not found",
			err:  fmt.Errorf("index ledger repo: %w", fmt.Errorf("process default branch: %w", fmt.Errorf("get child tree: %w", plumbing.ErrObjectNotFound))),
			want: true,
		},
		{
			// go-git's doubleIter flattens with %s, so the sentinel is not in the chain
			name: "observed diff tree from packfile not found (flattened)",
			err:  fmt.Errorf("index ledger repo: %w", fmt.Errorf("process default branch: %w", fmt.Errorf("diff tree: %w", fmt.Errorf("from: %s", dotgit.ErrPackfileNotFound)))),
			want: true,
		},
		{
			name: "packfile not found with sentinel intact",
			err:  fmt.Errorf("index ledger repo: %w", fmt.Errorf("diff tree: %w", dotgit.ErrPackfileNotFound)),
			want: true,
		},
		{
			name: "unrelated indexing failure",
			err:  fmt.Errorf("index ledger repo: %w", errors.New("parse symbols: unexpected token")),
			want: false,
		},
		{name: "nil", err: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isLedgerRepoChanged(tt.err))
		})
	}
}
