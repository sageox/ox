package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

// schemaPath is the published raw.jsonl line schema, relative to this package.
const schemaPath = "../../schema/v1/raw-jsonl.schema.json"

// schemaID must match the $id in the schema file. Kept as a literal so a
// careless $id edit fails a test rather than silently breaking every third
// party whose validator resolves the old URL.
const schemaID = "https://sageox.ai/schemas/session/v1/raw-jsonl.schema.json"

func compileRawJSONLSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()

	f, err := os.Open(schemaPath)
	require.NoError(t, err, "open published schema")
	defer f.Close()

	doc, err := jsonschema.UnmarshalJSON(f)
	require.NoError(t, err, "parse published schema as JSON")

	c := jsonschema.NewCompiler()
	require.NoError(t, c.AddResource(schemaID, doc), "add schema resource")

	sch, err := c.Compile(schemaID)
	require.NoError(t, err, "compile published schema")
	return sch
}

// validateFileLines validates every non-blank line of a JSONL file against the
// schema, reporting the line number on failure.
func validateFileLines(t *testing.T, sch *jsonschema.Schema, path string) int {
	t.Helper()

	f, err := os.Open(path)
	require.NoError(t, err, "open %s", path)
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Session lines carry whole tool outputs and routinely exceed bufio's 64KB default.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	lineNo, checked := 0, 0
	for scanner.Scan() {
		lineNo++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}

		var line any
		require.NoErrorf(t, json.Unmarshal([]byte(raw), &line),
			"%s:%d is not valid JSON", filepath.Base(path), lineNo)

		require.NoErrorf(t, sch.Validate(line),
			"%s:%d failed the published schema", filepath.Base(path), lineNo)
		checked++
	}
	require.NoError(t, scanner.Err(), "scan %s", path)
	return checked
}

// TestRawJSONLSchema_ValidatesShippedFixtures is the leg that proves the schema
// agrees with reality rather than merely with the prose spec. These three
// fixtures deliberately span all three on-disk dialects — native header +
// nested WriteEntry payloads, and the `_meta` import dialect with `ts` and
// 1-based seq. A schema that cannot read our own shipped fixtures is worthless.
//
// Failure prevented: publishing a schema at a public $id that rejects files ox
// itself produced.
func TestRawJSONLSchema_ValidatesShippedFixtures(t *testing.T) {
	sch := compileRawJSONLSchema(t)

	fixtures := []string{
		"testdata/standard_session.jsonl",
		"testdata/sample_session.jsonl",
		"testdata/imported_session.jsonl",
	}

	for _, fixture := range fixtures {
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			n := validateFileLines(t, sch, fixture)
			require.Positive(t, n, "fixture had no lines to check")
		})
	}
}

// TestRawJSONLSchema_ValidatesWriterOutput closes the loop the fixtures cannot:
// fixtures are checked-in snapshots that can drift from the writer, so this
// drives the real SessionWriter and validates what it actually emits today.
//
// Failure prevented: the writer gains a field or changes a shape and the
// published schema silently stops describing current output.
func TestRawJSONLSchema_ValidatesWriterOutput(t *testing.T) {
	sch := compileRawJSONLSchema(t)

	store, err := NewStore(t.TempDir())
	require.NoError(t, err, "new store")

	writer, err := store.CreateRaw("2026-01-06T14-32-tester-Ox7f3a")
	require.NoError(t, err, "create raw session")

	require.NoError(t, writer.WriteHeader(&StoreMeta{
		Version:      "1.0",
		CreatedAt:    time.Now().UTC(),
		AgentID:      "Ox7f3a",
		AgentType:    "claude-code",
		AgentVersion: "1.0.3",
		Model:        "claude-sonnet-4",
		Username:     "tester",
		RepoID:       "repo_01JEYQ9Z8X",
		OxVersion:    "0.9.0",
	}), "write header")

	// One of each documented entry shape, exercising the eid/seq/timestamp
	// injection path in WriteRaw.
	for _, entry := range []map[string]any{
		{"type": "user", "content": "Fix the failing test"},
		{"type": "assistant", "content": "Reading the test file."},
		{"type": "tool", "content": "", "tool_name": "bash", "tool_input": "go test ./...", "tool_output": "ok"},
		{"type": "system", "content": "Loaded coworker: code-reviewer", "coworker_name": "code-reviewer", "coworker_model": "sonnet"},
	} {
		require.NoError(t, writer.WriteRaw(entry), "write raw entry type=%v", entry["type"])
	}

	// Close writes the footer.
	require.NoError(t, writer.Close(), "close writer")

	n := validateFileLines(t, sch, writer.FilePath())
	require.Equal(t, 6, n, "expected header + 4 entries + footer")
}

// TestRawJSONLSchema_DocumentsEveryStoreMetaField is the anti-drift gate. The
// on-disk entry has no Go struct (WriteRaw takes map[string]any), so the schema
// is necessarily hand-written and reflection can only guard the one part that
// IS typed — the header metadata. Every StoreMeta json tag must appear in the
// schema's storeMeta properties.
//
// Failure prevented: a field added to StoreMeta ships to users' ledgers while
// the published schema still claims it doesn't exist.
func TestRawJSONLSchema_DocumentsEveryStoreMetaField(t *testing.T) {
	raw, err := os.ReadFile(schemaPath)
	require.NoError(t, err, "read published schema")

	var doc struct {
		Defs struct {
			StoreMeta struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"storeMeta"`
		} `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc), "parse published schema")
	require.NotEmpty(t, doc.Defs.StoreMeta.Properties, "schema has no storeMeta properties")

	typ := reflect.TypeOf(StoreMeta{})
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		require.Containsf(t, doc.Defs.StoreMeta.Properties, name,
			"StoreMeta field %q is written to raw.jsonl but missing from %s "+
				"($defs.storeMeta.properties) — add it there", name, schemaPath)
	}
}

// TestRawJSONLSchema_RejectsMalformedLines pins the few things the schema does
// constrain. Without this, "everything validates" could mean the schema is
// correct or that it is vacuous; these cases distinguish the two.
func TestRawJSONLSchema_RejectsMalformedLines(t *testing.T) {
	sch := compileRawJSONLSchema(t)

	cases := []struct {
		name string
		line string
	}{
		{"header without metadata", `{"type":"header"}`},
		{"entry without type", `{"content":"orphaned","seq":0}`},
		{"eid wrong length", `{"type":"user","content":"hi","eid":"toolong"}`},
		{"eid non-alphanumeric", `{"type":"user","content":"hi","eid":"ab-de"}`},
		{"negative seq", `{"type":"user","content":"hi","seq":-1}`},
		{"timestamp not RFC3339", `{"type":"user","content":"hi","timestamp":"last tuesday"}`},
		{"footer entry_count not an integer", `{"type":"footer","entry_count":"42"}`},
		// version and created_at are the only StoreMeta fields without
		// omitempty, so the native writer always emits them; a header missing
		// them is truncated or hand-rolled, not merely sparse.
		{"native header with empty metadata", `{"type":"header","metadata":{}}`},
		{"native header missing created_at", `{"type":"header","metadata":{"version":"1.0"}}`},
		// The dialect-overload guard: in a NATIVE header, session_id is the
		// ses_ recording identity. The import dialect overloads the same key
		// as an agent identifier, and conflating them misattributes a session
		// to the wrong recording — so the native side pins the prefix.
		{"native header session_id without ses_ prefix",
			`{"type":"header","metadata":{"version":"1.0","session_id":"test-session-001"}}`},
		{"native header continuation without ses_ prefix",
			`{"type":"header","metadata":{"version":"1.0","created_at":"2026-01-01T00:00:00Z","continued_from_session_id":"test-session-001"}}`},
		{"not an object", `"just a string"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var line any
			require.NoError(t, json.Unmarshal([]byte(tc.line), &line), "test case is not valid JSON")
			require.Error(t, sch.Validate(line), "schema accepted a line it should reject")
		})
	}
}
