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
