package receiver

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

const sessionA = "12345678-1234-4234-8234-123456789abc"
const sessionB = "87654321-4321-4321-8321-cba987654321"

func span(id string) string {
	return `{"name":"test","attributes":[{"key":"session.id","value":{"stringValue":"` + id + `"}}]}`
}

// Missing and malformed IDs must never leak into another session's trace file.
func TestDemuxSeparatesEveryRecord(t *testing.T) {
	t.Parallel()
	for _, signal := range []string{"traces", "logs"} {
		t.Run(signal, func(t *testing.T) {
			resources, scopes, records := signalKeys(signal)
			body := []byte(`{"extra":"retained","` + resources + `":[{"resource":{"attributes":[]},"schemaUrl":"resource-schema","` + scopes + `":[{"scope":{"name":"claude"},"schemaUrl":"scope-schema","` + records + `":[` + span(sessionA) + `,` + span(sessionB) + `,` + span("../../escape") + `,{"name":"missing"}]}]}]}`)
			parts, err := demux(body, signal)
			require.NoError(t, err)
			require.Len(t, parts, 3)
			for _, id := range []string{sessionA, sessionB, unattributed} {
				var decoded map[string]any
				require.NoError(t, json.Unmarshal(parts[id], &decoded))
				require.Equal(t, "retained", decoded["extra"])
				r := decoded[resources].([]any)[0].(map[string]any)
				require.Equal(t, "resource-schema", r["schemaUrl"])
				s := r[scopes].([]any)[0].(map[string]any)
				require.Equal(t, "scope-schema", s["schemaUrl"])
				want := 1
				if id == unattributed {
					want = 2
				}
				require.Len(t, s[records], want)
				if id != unattributed {
					require.Contains(t, string(parts[id]), id)
					require.NotContains(t, string(parts[id]), "escape")
					require.NotContains(t, string(parts[id]), "missing")
				}
			}
		})
	}
}

// Session IDs can live on resources, scopes, or records. Invalid explicit IDs
// must not accidentally inherit a neighboring valid attribution.
func TestDemuxAttributeLocationsAndValidation(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, body, id string }{
		{"resource", `{"resourceSpans":[{"resource":` + span(sessionA) + `,"scopeSpans":[{"spans":[{"name":"inherits"}]}]}]}`, sessionA},
		{"scope", `{"resourceSpans":[{"scopeSpans":[{"scope":` + span(sessionA) + `,"spans":[{"name":"inherits"}]}]}]}`, sessionA},
		{"upper case", string(payload("traces", "12345678-1234-4234-8234-123456789ABC")), sessionA},
		{"noncanonical", string(payload("traces", "12345678123442348234123456789abc")), unattributed},
		{"missing", `{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"missing"}]}]}]}`, unattributed},
		{"invalid override", `{"resourceSpans":[{"resource":` + span(sessionA) + `,"scopeSpans":[{"spans":[` + span("invalid") + `]}]}]}`, unattributed},
		{"malformed attributes", `{"resourceSpans":[{"scopeSpans":[{"spans":[{"attributes":42}]}]}]}`, unattributed},
		{"conflicting ids", `{"resourceSpans":[{"scopeSpans":[{"spans":[{"attributes":[{"key":"session.id","value":{"stringValue":"` + sessionA + `"}},{"key":"session.id","value":{"stringValue":"` + sessionB + `"}}]}]}]}]}`, unattributed},
		{"unrelated attributes", `{"resourceSpans":[{"scopeSpans":[{"spans":[{"attributes":[{"key":"other","value":{"stringValue":"` + sessionA + `"}}]}]}]}]}`, unattributed},
		{"empty request", `{}`, unattributed},
		{"empty resource", `{"resourceSpans":[{}]}`, unattributed},
		{"empty scope", `{"resourceSpans":[{"scopeSpans":[{}]}]}`, unattributed},
		{"invalid resource attributes", `{"resourceSpans":[{"resource":42,"scopeSpans":[{"spans":[{}]}]}]}`, unattributed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			parts, err := demux([]byte(tt.body), "traces")
			require.NoError(t, err)
			require.Len(t, parts, 1)
			require.Equal(t, tt.body, string(parts[tt.id]))
		})
	}
}

func TestMalformedOTLPWrappersRejected(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`[]`, `null`, `{`, `{"resourceSpans":42}`, `{"resourceSpans":[null]}`,
		`{"resourceSpans":[{"scopeSpans":42}]}`, `{"resourceSpans":[{"scopeSpans":[null]}]}`,
		`{"resourceSpans":[{"scopeSpans":[{"spans":42}]}]}`, `{"resourceSpans":[{"scopeSpans":[{"spans":[null]}]}]}`,
	} {
		_, err := demux([]byte(body), "traces")
		require.Error(t, err, body)
	}
}
