package selfexec

import (
	"errors"
	"os"
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

// TestResolve_ReturnsExecutableOutsideTests covers the branch Path takes in a
// real ox process, which testing.Testing() makes unreachable from a test.
func TestResolve_ReturnsExecutableOutsideTests(t *testing.T) {
	path, err := resolve(false)

	if err != nil {
		t.Fatalf("resolve(false): unexpected error %v", err)
	}
	want, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable(): %v", err)
	}
	if path != want {
		t.Errorf("resolve(false) = %q, want %q", path, want)
	}
}

func TestResolve_RefusesWhenUnderTest(t *testing.T) {
	path, err := resolve(true)

	if !errors.Is(err, ErrUnderTest) {
		t.Fatalf("resolve(true): got err %v, want ErrUnderTest", err)
	}
	if path != "" {
		t.Errorf("resolve(true): got path %q, want empty", path)
	}
}
