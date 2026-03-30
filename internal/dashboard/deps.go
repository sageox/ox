package dashboard

import "github.com/sageox/ox/internal/dashboard/effects"

// Deps holds injected dependencies for the dashboard.
type Deps struct {
	Client effects.Client
}

// DefaultDeps returns production dependencies wired to the live daemon IPC and session store.
func DefaultDeps() Deps {
	return Deps{
		Client: effects.NewCLIClient(),
	}
}
