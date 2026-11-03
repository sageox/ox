package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/secretguard"
	"github.com/stretchr/testify/require"
)

func TestSaveRedactsPlanBeforePersistence(t *testing.T) {
	for _, canary := range []string{"AKIA" + "QWERTYUIOPASDFGH", "oxb_" + "syntheticToken123456789"} {
		t.Run(canary[:4], func(t *testing.T) {
			ledger := t.TempDir()
			withLedger(t, ledger)
			in := Parse("# Deployment plan\n\nUse " + canary + " for deployment.\n")
			meta := Meta{Topic: "Deploy with " + canary, CreatedAt: time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)}
			preview, err := ResolveSaveDir("/fake/root", meta)
			require.NoError(t, err)
			dir, _, err := Save("/fake/root", in, Result{}, nil, meta)
			require.NoError(t, err)
			require.Equal(t, preview, dir, "prior-revision lookup must use the redacted slug too")
			want, _ := secretguard.RedactText(in.Raw)
			require.NotEqual(t, in.Raw, want, "fixture must exercise redaction")
			got, err := os.ReadFile(filepath.Join(dir, planMDFile))
			require.NoError(t, err)
			require.Equal(t, want, string(got))
			for _, name := range []string{planMDFile, annotationsFile, planMetaFile, eventsFile} {
				data, err := os.ReadFile(filepath.Join(dir, name))
				require.NoError(t, err)
				require.NotContains(t, string(data), canary)
				require.NoError(t, secretguard.ValidateContent(name, data))
			}
			require.NotContains(t, filepath.Base(dir), canary)
		})
	}
}

func TestSaveRefusesUnsafeCompanionsBeforeWriting(t *testing.T) {
	const canary = "oxb_" + "syntheticToken123456789"
	for _, name := range []string{annotationsFile, planMetaFile, planHTMLFile} {
		t.Run(name, func(t *testing.T) {
			ledger := t.TempDir()
			withLedger(t, ledger)
			res := Result{}
			meta := Meta{Topic: "Deployment plan"}
			var html []byte
			switch name {
			case annotationsFile:
				res.Guidance = canary
			case planMetaFile:
				meta.Authors = []string{canary}
			case planHTMLFile:
				html = []byte("<html><body>" + canary + "</body></html>")
			}
			dir, _, err := Save("/fake/root", Parse("# Deploy\n"+canary), res, html, meta)
			require.ErrorContains(t, err, name+" refused")
			require.NotContains(t, err.Error(), canary)
			require.Empty(t, dir)
			entries, err := os.ReadDir(ledger)
			require.NoError(t, err)
			require.Empty(t, entries, "refused saves must not leave partial artifacts")
		})
	}
}

func TestPlanHTMLUploadRefusesSecretsBeforeNetwork(t *testing.T) {
	const canary = "oxb_" + "syntheticToken123456789"
	page := []byte(strings.Repeat(" ", htmlLFSThreshold) + "<p>" + canary + "</p>")
	_, uploaded, err := UploadHTML(&lfs.Client{}, page)
	require.ErrorContains(t, err, "upload plan.html refused")
	require.NotContains(t, err.Error(), canary)
	require.False(t, uploaded)
}
