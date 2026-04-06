package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sageox/ox/internal/flags"
)

func TestHandleSettingsGet_WithCachedSettings(t *testing.T) {
	s := NewServer(nil)

	want := &flags.CLISettingsResponse{
		Features: flags.CLIFeatures{
			CodeDB:  boolPtrSettings(true),
			Whisper: boolPtrSettings(false),
		},
		Killswitches: flags.CLIKillswitches{
			DisableFileDeleteTools: true,
		},
		FetchedAt: time.Now(),
	}

	s.SetSettingsGetHandler(func() *flags.CLISettingsResponse {
		return want
	})

	result := handleSettingsGet(s, Message{}, nil)
	if result.Response == nil {
		t.Fatal("expected non-nil response")
	}
	if !result.Response.Success {
		t.Fatalf("expected success, got error: %s", result.Response.Error)
	}

	var got flags.CLISettingsResponse
	if err := json.Unmarshal(result.Response.Data, &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.Features.CodeDB == nil || !*got.Features.CodeDB {
		t.Error("expected CodeDB=true")
	}
	if got.Features.Whisper == nil || *got.Features.Whisper {
		t.Error("expected Whisper=false")
	}
	if !got.Killswitches.DisableFileDeleteTools {
		t.Error("expected DisableFileDeleteTools=true")
	}
}

func TestHandleSettingsGet_NilSettings(t *testing.T) {
	s := NewServer(nil)

	s.SetSettingsGetHandler(func() *flags.CLISettingsResponse {
		return nil
	})

	result := handleSettingsGet(s, Message{}, nil)
	if result.Response == nil {
		t.Fatal("expected non-nil response")
	}
	if !result.Response.Success {
		t.Fatalf("expected success, got error: %s", result.Response.Error)
	}
	if string(result.Response.Data) != `null` {
		t.Errorf("expected null data, got %s", result.Response.Data)
	}
}

func TestHandleSettingsGet_NoHandler(t *testing.T) {
	s := NewServer(nil)
	// don't set handler — CallbackService returns nil

	result := handleSettingsGet(s, Message{}, nil)
	if result.Response == nil {
		t.Fatal("expected non-nil response")
	}
	if !result.Response.Success {
		t.Fatalf("expected success (nil = no settings yet), got error: %s", result.Response.Error)
	}
	if string(result.Response.Data) != `null` {
		t.Errorf("expected null data, got %s", result.Response.Data)
	}
}

// boolPtrSettings avoids conflict with other test files in the package
func boolPtrSettings(b bool) *bool { return &b }
