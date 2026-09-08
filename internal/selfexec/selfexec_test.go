package selfexec

import (
	"errors"
	"testing"
)

// TestPath_RefusesUnderTest is the regression gate for the recursive-test-binary
// fork bomb: this test runs inside a test binary, so Path must refuse.
//
// Red-first proof: delete the testing.Testing() branch in Path and this test
// fails with a real .../ox.test path — which is exactly the path that, when
// exec'd as `ox.test daemon start`, re-runs this whole suite forever.
func TestPath_RefusesUnderTest(t *testing.T) {
	path, err := Path()

	if !errors.Is(err, ErrUnderTest) {
		t.Fatalf("Path() under go test: got err %v, want ErrUnderTest", err)
	}
	if path != "" {
		t.Errorf("Path() under go test: got path %q, want empty", path)
	}
}
