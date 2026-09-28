package read

import (
	"strings"
	"testing"
)

const (
	fullCnv  = "cnv_019ff2f5-2079-7be1-b05e-8caad2772e61"
	fullRec  = "rec_019ff2f5-2079-7be1-b05e-8caad2772e61"
	fullClyr = "clyr_019ff2f5-deb5-77d3-b84b-04db14c601ca"
)

func TestParseID(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantRec string
		wantCnv string
		wantURI bool
		wantErr string
	}{
		{name: "cnv id", raw: fullCnv, wantRec: fullRec, wantCnv: fullCnv},
		{name: "rec id", raw: fullRec, wantRec: fullRec, wantCnv: fullCnv},
		{
			name:    "citation URI with selectors",
			raw:     "sageox://" + fullCnv + "/" + fullClyr + "@2#cue=5-6",
			wantRec: fullRec, wantCnv: fullCnv, wantURI: true,
		},
		{name: "bare UUID rejected", raw: "019ff2f5-2079-7be1-b05e-8caad2772e61", wantErr: ErrCodeInvalidID},
		{name: "folder name rejected", raw: "2026-08-11-22-32-full", wantErr: ErrCodeInvalidID},
		{name: "uuid prefix rejected", raw: "cnv_019ff2f5", wantErr: ErrCodeInvalidID},
		{name: "uppercase rejected", raw: strings.ToUpper(fullCnv), wantErr: ErrCodeInvalidID},
		{name: "non-v7 rejected", raw: "cnv_019ff2f5-2079-4be1-b05e-8caad2772e61", wantErr: ErrCodeInvalidID},
		{name: "wrong variant rejected", raw: "cnv_019ff2f5-2079-7be1-705e-8caad2772e61", wantErr: ErrCodeInvalidID},
		{name: "smuggled layer path rejected", raw: "cnv_019ff2f5-2079-7be1-b05e-8caad2772e61/" + fullClyr, wantErr: ErrCodeInvalidID},
		{name: "smuggled selector rejected", raw: "rec_019ff2f5-2079-7be1-b05e-8caad2772e61#cue=1", wantErr: ErrCodeInvalidID},
		{name: "malformed URI", raw: "sageox://rec_019ff2f5-2079-7be1-b05e-8caad2772e61", wantErr: ErrCodeInvalidID},
		{name: "empty", raw: "", wantErr: ErrCodeInvalidID},
		{name: "tp id is not a conversation id", raw: "tp_019ff2f5-2079-7be1-b05e-8caad2772e61", wantErr: ErrCodeInvalidID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := ParseID(tt.raw)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ParseID(%q) succeeded, want error %s", tt.raw, tt.wantErr)
				}
				if err.Code != tt.wantErr {
					t.Fatalf("ParseID(%q) error code = %s, want %s", tt.raw, err.Code, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseID(%q) failed: %v", tt.raw, err)
			}
			if id.RecordingID != tt.wantRec || id.ConversationID != tt.wantCnv {
				t.Errorf("ParseID(%q) = (%s, %s), want (%s, %s)", tt.raw, id.RecordingID, id.ConversationID, tt.wantRec, tt.wantCnv)
			}
			if (id.Address != nil) != tt.wantURI {
				t.Errorf("ParseID(%q) Address presence = %v, want %v", tt.raw, id.Address != nil, tt.wantURI)
			}
		})
	}
}

// TestParseIDTrimsBareIDs: a bare id pasted with a trailing newline is the
// same id.
func TestParseIDTrimsBareIDs(t *testing.T) {
	id, err := ParseID(" " + fullCnv + "\n")
	if err != nil || id.RecordingID != fullRec {
		t.Fatalf("ParseID(padded cnv) = %+v, %v", id, err)
	}
}

func TestParseIDCarriesSelectors(t *testing.T) {
	id, err := ParseID("sageox://" + fullCnv + "/" + fullClyr + "@2#cue=5-6")
	if err != nil {
		t.Fatalf("ParseID failed: %v", err)
	}
	if id.Address.Revision != 2 {
		t.Errorf("Revision = %d, want 2", id.Address.Revision)
	}
	if c := id.Address.Selectors.Cue; c == nil || c.From != 5 || c.To != 6 {
		t.Errorf("Cue selector = %+v, want 5-6", id.Address.Selectors.Cue)
	}
}

func TestValidateTopicID(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "valid", raw: "tp_01a012cb-9764-7555-a3f3-ce3377e47d98"},
		{name: "title rejected", raw: "Hiring", wantErr: true},
		{name: "ordinal rejected", raw: "1", wantErr: true},
		{name: "cnv prefix rejected", raw: fullCnv, wantErr: true},
		{name: "truncated rejected", raw: "tp_01a012cb", wantErr: true},
		{name: "non-hex rejected", raw: "tp_019ff500-0000-7000-8000-00000000tp01", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTopicID(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateTopicID(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if err != nil && err.Code != ErrCodeInvalidID {
				t.Errorf("error code = %s, want %s", err.Code, ErrCodeInvalidID)
			}
		})
	}
}

// TestParseIDLinks covers pasted sageox.ai links. Failure prevented: an AI
// coworker handed a recording link cannot open it (it used to be invalid_id),
// or a lookalike/foreign host smuggles an id past the host check.
func TestParseIDLinks(t *testing.T) {
	const team = "team_01a0e91c"
	tests := []struct {
		name     string
		raw      string
		wantRec  string
		wantHost string
		wantErr  string
	}{
		{name: "short link rec", raw: "https://sageox.ai/c/" + fullRec, wantRec: fullRec, wantHost: "sageox.ai"},
		{name: "short link cnv", raw: "https://sageox.ai/c/" + fullCnv, wantRec: fullRec, wantHost: "sageox.ai"},
		{name: "http scheme", raw: "http://sageox.ai/c/" + fullRec, wantRec: fullRec, wantHost: "sageox.ai"},
		{name: "uppercase scheme and host", raw: "HTTPS://SageOx.AI/c/" + fullRec, wantRec: fullRec, wantHost: "sageox.ai"},
		{name: "trailing slash", raw: "https://sageox.ai/c/" + fullRec + "/", wantRec: fullRec, wantHost: "sageox.ai"},
		{name: "query and fragment ignored", raw: "https://sageox.ai/c/" + fullRec + "?utm=x#t=12", wantRec: fullRec, wantHost: "sageox.ai"},
		{name: "recording page", raw: "https://sageox.ai/team/" + team + "/media/recordings/" + fullRec, wantRec: fullRec, wantHost: "sageox.ai"},
		{name: "recording transcript tab", raw: "https://sageox.ai/team/" + team + "/media/recordings/" + fullRec + "/transcript", wantRec: fullRec, wantHost: "sageox.ai"},
		{name: "kb recording", raw: "https://sageox.ai/kb/kb_1/recordings/" + fullRec, wantRec: fullRec, wantHost: "sageox.ai"},
		{name: "test subdomain kept", raw: "https://test.sageox.ai/c/" + fullRec, wantRec: fullRec, wantHost: "test.sageox.ai"},
		{name: "www prefix normalized", raw: "https://www.sageox.ai/c/" + fullRec, wantRec: fullRec, wantHost: "sageox.ai"},
		{name: "app prefix on test normalized", raw: "https://app.test.sageox.ai/c/" + fullRec, wantRec: fullRec, wantHost: "test.sageox.ai"},
		{name: "pasted with surrounding whitespace", raw: "  https://sageox.ai/c/" + fullRec + "\n", wantRec: fullRec, wantHost: "sageox.ai"},
		{name: "pasted with CRLF", raw: "https://sageox.ai/c/" + fullRec + "\r\n", wantRec: fullRec, wantHost: "sageox.ai"},
		{name: "trailing-dot FQDN", raw: "https://sageox.ai./c/" + fullRec, wantRec: fullRec, wantHost: "sageox.ai"},
		{name: "trailing-dot subdomain", raw: "https://test.sageox.ai./c/" + fullRec, wantRec: fullRec, wantHost: "test.sageox.ai"},
		{name: "trailing-dot lookalike", raw: "https://sageox.ai.evil.com./c/" + fullRec, wantErr: ErrCodeInvalidID},
		{name: "foreign host", raw: "https://example.com/c/" + fullRec, wantErr: ErrCodeInvalidID},
		{name: "lookalike suffix host", raw: "https://sageox.ai.evil.com/c/" + fullRec, wantErr: ErrCodeInvalidID},
		{name: "lookalike prefix host", raw: "https://evilsageox.ai/c/" + fullRec, wantErr: ErrCodeInvalidID},
		{name: "userinfo trick", raw: "https://sageox.ai@evil.com/c/" + fullRec, wantErr: ErrCodeInvalidID},
		{name: "share link", raw: "https://sageox.ai/s/rs-abc123", wantErr: ErrCodeShareLinkUnresolvable},
		{name: "junk path", raw: "https://sageox.ai/pricing", wantErr: ErrCodeInvalidID},
		{name: "root path", raw: "https://sageox.ai/", wantErr: ErrCodeInvalidID},
		{name: "short link with extra segment", raw: "https://sageox.ai/c/" + fullRec + "/x", wantErr: ErrCodeInvalidID},
		{name: "valid link wrapping bad uuid", raw: "https://sageox.ai/c/rec_019ff2f5-2079-4be1-b05e-8caad2772e61", wantErr: ErrCodeInvalidID},
		{name: "valid link wrapping non-id segment", raw: "https://sageox.ai/c/ses_019ff2f5-2079-7be1-b05e-8caad2772e61", wantErr: ErrCodeInvalidID},
		{name: "recording path wrapping topic id", raw: "https://sageox.ai/kb/kb_1/recordings/tp_019ff2f5-2079-7be1-b05e-8caad2772e61", wantErr: ErrCodeInvalidID},
		{name: "encoded traversal segment", raw: "https://sageox.ai/c/..%2F" + fullRec, wantErr: ErrCodeInvalidID},
		{name: "no host", raw: "https:///c/" + fullRec, wantErr: ErrCodeInvalidID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := ParseID(tt.raw)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ParseID(%q) = %+v, want error %s", tt.raw, id, tt.wantErr)
				}
				if err.Code != tt.wantErr {
					t.Fatalf("ParseID(%q) code = %s (%s), want %s", tt.raw, err.Code, err.Message, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseID(%q) failed: %v", tt.raw, err)
			}
			if id.RecordingID != tt.wantRec {
				t.Errorf("RecordingID = %s, want %s", id.RecordingID, tt.wantRec)
			}
			if id.LinkHost != tt.wantHost {
				t.Errorf("LinkHost = %q, want %q", id.LinkHost, tt.wantHost)
			}
		})
	}
}

// TestParseIDLinkErrorTruncatesInput: a hostile multi-KB link must not be
// echoed back whole into the envelope (and so into agent context).
func TestParseIDLinkErrorTruncatesInput(t *testing.T) {
	raw := "https://example.com/" + strings.Repeat("a", 4096)
	_, err := ParseID(raw)
	if err == nil {
		t.Fatal("foreign link accepted")
	}
	if strings.Contains(err.Message, strings.Repeat("a", 200)) {
		t.Errorf("error message reproduces untrusted input unbounded: %d bytes", len(err.Message))
	}
}

// TestInvalidIDMessageNamesLinkForms: the invalid_id message is the only
// place a caller learns links are accepted.
func TestInvalidIDMessageNamesLinkForms(t *testing.T) {
	_, err := ParseID("not-an-id")
	if err == nil || !strings.Contains(err.Message, "sageox.ai/c/rec_") {
		t.Fatalf("invalid_id message does not name link forms: %v", err)
	}
}
