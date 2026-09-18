package codexhistory

import (
	"context"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/sessionhistory"
)

type Adapter struct{}

var _ sessionhistory.Adapter = Adapter{}

func (Adapter) Name() string                           { return "codex" }
func (Adapter) ParserVersion() string                  { return ParserVersion }
func (Adapter) Home() (string, error)                  { return Home() }
func (Adapter) Discover(home string) ([]string, error) { return Discover(home) }
func (Adapter) Inspect(path string) (Snapshot, error)  { return Inspect(path) }
func (Adapter) Stream(ctx context.Context, path string, emit func(adapterprotocol.RawEntry) error) (Snapshot, error) {
	return Stream(ctx, path, emit)
}
