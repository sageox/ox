package agents_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// globCorpus records what real Claude Code loaded for each (glob, path) pair.
type globCorpus struct {
	ClaudeVersion string          `json:"claude_version"`
	Note          string          `json:"note"`
	Rows          []globCorpusRow `json:"rows"`
}

type globCorpusRow struct {
	Glob           string `json:"glob"`
	Path           string `json:"path"`
	Loads          bool   `json:"loads"`
	Construct      string `json:"construct"`
	AsWritten      bool   `json:"as_written,omitempty"`
	AtSessionStart bool   `json:"at_session_start,omitempty"`
}

func readGlobCorpus(t *testing.T, path string) globCorpus {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var corpus globCorpus
	if err := decoder.Decode(&corpus); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	return corpus
}

// TestGlobCorpus_CoversEveryConstruct keeps the corpus from being trimmed into something
// that proves nothing: each construct has to show both a load and a miss.
func TestGlobCorpus_CoversEveryConstruct(t *testing.T) {
	t.Parallel()
	corpus := readGlobCorpus(t, "testdata/glob-conformance.json")
	if corpus.ClaudeVersion == "" {
		t.Fatal("the corpus does not say which Claude Code it was derived on")
	}
	seen := map[string]map[bool]int{}
	for _, row := range corpus.Rows {
		if seen[row.Construct] == nil {
			seen[row.Construct] = map[bool]int{}
		}
		seen[row.Construct][row.Loads]++
	}
	for _, construct := range []string{"route-group", "bracket", "brace", "brace-as-written", "double-star", "dotfile"} {
		if seen[construct][true] == 0 || seen[construct][false] == 0 {
			t.Errorf("construct %q needs a row that loads and a row that does not, got %v", construct, seen[construct])
		}
	}
}
