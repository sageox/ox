package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sageox/ox/internal/session/cursorpaths"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

func runCursorServeRequests(t *testing.T, requests ...adapterprotocol.Request) ([]adapterprotocol.Response, *adapterruntime.Server) {
	t.Helper()
	var input bytes.Buffer
	encoder := json.NewEncoder(&input)
	for _, request := range requests {
		if err := encoder.Encode(request); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	server := adapterruntime.NewServer(&input, &output)
	handleServe(server)

	decoder := json.NewDecoder(&output)
	responses := make([]adapterprotocol.Response, 0, len(requests))
	for {
		var response adapterprotocol.Response
		if err := decoder.Decode(&response); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode serve response: %v; raw=%q", err, output.String())
		}
		responses = append(responses, response)
	}
	return responses, server
}

func requestParams(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decodeServeResult[T any](t *testing.T, response adapterprotocol.Response) T {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("serve response error: %+v", response.Error)
	}
	data, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode result: %v; raw=%s", err, data)
	}
	return result
}

func TestCursorServeReadMatchesOneShotAndKeepsSessionsIsolated(t *testing.T) {
	// These are explicit synthetic inputs used only to exercise process-local
	// routing. Real exported record shapes are covered in conformance_test.go.
	fileA := cursorReaderFile(t, []byte("{\"role\":\"user\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"session A\"}]}}\n"))
	fileB := cursorReaderFile(t, []byte("{\"role\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"session B\"}]}}\n"))
	requests := []adapterprotocol.Request{
		{ID: 1, Method: adapterprotocol.MethodReadFromOffset, Params: requestParams(t, adapterprotocol.ReadFromOffsetParams{AgentID: "OxA", SessionFile: fileA})},
		{ID: 2, Method: adapterprotocol.MethodReadFromOffset, Params: requestParams(t, adapterprotocol.ReadFromOffsetParams{AgentID: "OxB", SessionFile: fileB})},
		{ID: 3, Method: adapterprotocol.MethodReadFromOffset, Params: requestParams(t, adapterprotocol.ReadFromOffsetParams{AgentID: "OxA", SessionFile: fileA})},
		{ID: 4, Method: adapterprotocol.MethodEndSession, Params: requestParams(t, adapterprotocol.EndSessionParams{AgentID: "OxA"})},
		{ID: 5, Method: adapterprotocol.MethodShutdown},
	}
	responses, server := runCursorServeRequests(t, requests...)
	if len(responses) != len(requests) {
		t.Fatalf("responses = %d, want %d", len(responses), len(requests))
	}
	for index, file := range []string{fileA, fileB, fileA} {
		got := decodeServeResult[adapterprotocol.ReadFromOffsetResult](t, responses[index])
		want, err := handleReadFromOffset(adapterprotocol.ReadFromOffsetParams{SessionFile: file})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Entries, want.Entries) || got.NewOffset != want.NewOffset {
			t.Fatalf("response %d = %+v, want %+v", index, got, want)
		}
	}
	if responses[3].Error != nil || responses[4].Error != nil {
		t.Fatalf("end/shutdown responses = %+v", responses[3:])
	}
	if !errors.Is(server.Context().Err(), context.Canceled) {
		t.Fatalf("server context = %v, want canceled", server.Context().Err())
	}
}

func TestCursorServeFindSessionUnknownMethodAndShutdown(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".sageox", "config.json"), []byte(`{"repo_id":"serve-fixture"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	conversationID := "11111111-1111-1111-1111-111111111111"
	source, err := cursorpaths.SessionPath(home, repo, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("{\"type\":\"turn_ended\",\"status\":\"success\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	responses, _ := runCursorServeRequests(t,
		adapterprotocol.Request{ID: 10, Method: adapterprotocol.MethodFindSession, Params: requestParams(t, adapterprotocol.FindSessionParams{AgentID: "OxA", RepoRoot: repo, AgentSessionID: conversationID})},
		adapterprotocol.Request{ID: 11, Method: "future-method", Params: json.RawMessage(`{}`)},
		adapterprotocol.Request{ID: 12, Method: adapterprotocol.MethodShutdown},
	)
	if len(responses) != 3 {
		t.Fatalf("responses = %+v", responses)
	}
	find := decodeServeResult[adapterprotocol.FindSessionResult](t, responses[0])
	if find.SessionFile != source || find.Offset != 0 {
		t.Fatalf("find result = %+v", find)
	}
	if responses[1].Error == nil || responses[1].Error.Code != adapterprotocol.ErrCodeMethodNotFound {
		t.Fatalf("unknown response = %+v", responses[1])
	}
	if responses[2].Error != nil {
		t.Fatalf("shutdown response = %+v", responses[2])
	}
}
