package session

import (
	"encoding/json"
	"testing"
)

// FuzzParseHistoryEntry exercises the untrusted JSONL boundary shared by
// imported planning history and reconstructed AI coworker sessions. Successful
// parses must classify a line as exactly one shape, and that classification
// must survive a JSON round trip.
func FuzzParseHistoryEntry(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"_meta":{"schema_version":"1","agent_id":"OxTest","source":"agent_reconstruction"}}`),
		[]byte(`{"type":"user","content":"hello","seq":1}`),
		[]byte(`{"type":"tool","tool_name":"Read","seq":2}`),
		[]byte(`null`),
		[]byte(`{"_meta":`),
		{0xff, 0xfe, 0xfd},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, line []byte) {
		meta, entry, err := ParseHistoryEntry(line)
		if err != nil {
			return
		}
		if (meta == nil) == (entry == nil) {
			t.Fatalf("successful parse classified as meta=%t entry=%t", meta != nil, entry != nil)
		}

		var encoded []byte
		if meta != nil {
			var marshalErr error
			encoded, marshalErr = json.Marshal(map[string]any{"_meta": meta})
			if marshalErr != nil {
				t.Fatalf("marshal parsed metadata: %v", marshalErr)
			}
		} else {
			_ = ValidateHistoryEntry(entry) // validation must remain panic-free for any parsed shape
			var marshalErr error
			encoded, marshalErr = json.Marshal(entry)
			if marshalErr != nil {
				t.Fatalf("marshal parsed entry: %v", marshalErr)
			}
		}

		roundMeta, roundEntry, roundErr := ParseHistoryEntry(encoded)
		if roundErr != nil {
			t.Fatalf("round-trip parse: %v", roundErr)
		}
		if (meta != nil) != (roundMeta != nil) || (entry != nil) != (roundEntry != nil) {
			t.Fatalf("classification changed after round trip")
		}
	})
}
