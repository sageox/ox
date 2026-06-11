package ledger

import (
	"os"
	"testing"

	"github.com/sageox/ox/internal/gitserver"
)

// TestMain opts the ledger test binary into git's file/local transport. These
// tests clone from local bare repos (file:// and bare /tmp paths) to simulate a
// remote. Production hardens against that transport — CloneWithSparseCheckout
// passes `-c protocol.file.allow=never` (HardenedCloneArgs) and rejects
// local/file URLs in gitutil.ValidateCloneURL — both gated on
// gitserver.TestAllowFileTransport. Tests must opt back in, mirroring the daemon
// suite's isolateCredentials helper.
func TestMain(m *testing.M) {
	gitserver.TestAllowFileTransport = true
	os.Exit(m.Run())
}
