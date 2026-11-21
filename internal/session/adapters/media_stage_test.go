package adapters

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/sageox/ox/internal/mediapool"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// The bytes stop at the adapter layer: staged on disk by digest, absent from
// the entry that flows on to raw.jsonl.
func TestProtocolToInternalStagesImageBytesAndDropsThem(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	img := []byte("\x89PNG-pixels")
	sum := sha256.Sum256(img)
	sha := hex.EncodeToString(sum[:])

	out := protocolToInternal([]adapterprotocol.RawEntry{
		{Role: adapterprotocol.RoleMedia, Media: &adapterprotocol.MediaRef{SHA256: sha, MIME: "image/png", Size: int64(len(img)), Data: img}},
		{Role: adapterprotocol.RoleMedia, Media: &adapterprotocol.MediaRef{SHA256: "00", MIME: "image/png", Size: 1, Data: []byte("x")}},
	})
	if out[0].Media == nil || out[0].Media.Skipped != "" || len(out[0].Media.Data) != 0 {
		t.Fatalf("staged ref = %+v", out[0].Media)
	}
	got, err := mediapool.Read("", sha)
	if err != nil || string(got) != string(img) {
		t.Fatalf("staged bytes = %q, %v", got, err)
	}
	// bytes that do not match their digest are never staged, and say so
	if out[1].Media.Skipped == "" || len(out[1].Media.Data) != 0 {
		t.Fatalf("mismatched ref = %+v", out[1].Media)
	}
}
