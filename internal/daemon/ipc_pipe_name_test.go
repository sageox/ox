package daemon

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPipeNameIsStableAndPathScoped(t *testing.T) {
	first := pipeName(`/runtime/one/daemon.sock`)
	assert.Equal(t, first, pipeName(`/runtime/one/daemon.sock`))
	assert.NotEqual(t, first, pipeName(`/runtime/two/daemon.sock`))
	assert.True(t, strings.HasPrefix(first, "sageox-daemon-"))
	assert.Len(t, first, len("sageox-daemon-")+16)
}
