package receiver

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const unattributed = "_unattributed"

type object map[string]json.RawMessage

func signalKeys(signal string) (string, string, string) {
	if signal == "logs" {
		return "resourceLogs", "scopeLogs", "logRecords"
	}
	return "resourceSpans", "scopeSpans", "spans"
}

// ValidSessionID accepts the canonical UUID shape only, never path-like names.
func ValidSessionID(id string) bool {
	return len(id) == 36 && uuid.Validate(id) == nil
}

func cloneObject(src object) object {
	dst := make(object, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func encode(v any) json.RawMessage {
	// All values originate from validated JSON, so marshaling cannot fail.
	b, _ := json.Marshal(v)
	return b
}

func decodeObject(raw json.RawMessage) (object, error) {
	var obj object
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, errors.New("expected JSON object")
	}
	return obj, nil
}

// attributeSession distinguishes absent IDs (inherit) from invalid IDs (quarantine).
func attributeSession(obj object, inherited string) string {
	raw, ok := obj["attributes"]
	if !ok {
		return inherited
	}
	var attrs []struct {
		Key   string `json:"key"`
		Value struct {
			StringValue string `json:"stringValue"`
		} `json:"value"`
	}
	if json.Unmarshal(raw, &attrs) != nil {
		return unattributed
	}
	found := ""
	for _, attr := range attrs {
		if attr.Key != "session.id" {
			continue
		}
		id := strings.ToLower(attr.Value.StringValue)
		if !ValidSessionID(id) || (found != "" && found != id) {
			return unattributed
		}
		found = id
	}
	if found != "" {
		return found
	}
	return inherited
}

func wrapperSession(obj object, key, inherited string) string {
	raw, ok := obj[key]
	if !ok {
		return inherited
	}
	nested, err := decodeObject(raw)
	if err != nil {
		return unattributed
	}
	return attributeSession(nested, inherited)
}

// demux retains resource/scope metadata and unknown OTLP fields. Records without
// usable IDs are quarantined even when their batch also contains valid sessions.
func demux(body []byte, signal string) (map[string][]byte, error) {
	top, err := decodeObject(body)
	if err != nil {
		return nil, fmt.Errorf("decode OTLP request: %w", err)
	}
	resourceKey, scopeKey, recordKey := signalKeys(signal)
	var resources []object
	if raw, ok := top[resourceKey]; ok {
		if err := json.Unmarshal(raw, &resources); err != nil {
			return nil, fmt.Errorf("decode resources: %w", err)
		}
	}
	grouped := make(map[string][]object)
	for _, resource := range resources {
		if resource == nil {
			return nil, errors.New("null resource")
		}
		inherited := wrapperSession(resource, "resource", unattributed)
		var scopes []object
		if raw, ok := resource[scopeKey]; ok {
			if err := json.Unmarshal(raw, &scopes); err != nil {
				return nil, fmt.Errorf("decode scopes: %w", err)
			}
		}
		byResource := make(map[string][]object)
		for _, scope := range scopes {
			if scope == nil {
				return nil, errors.New("null scope")
			}
			scopeID := wrapperSession(scope, "scope", inherited)
			var records []object
			if raw, ok := scope[recordKey]; ok {
				if err := json.Unmarshal(raw, &records); err != nil {
					return nil, fmt.Errorf("decode records: %w", err)
				}
			}
			byScope := make(map[string][]object)
			for _, record := range records {
				if record == nil {
					return nil, errors.New("null record")
				}
				id := attributeSession(record, scopeID)
				byScope[id] = append(byScope[id], record)
			}
			// Keep empty wrappers when splitting; they carry no attributable records.
			if len(records) == 0 {
				byScope[unattributed] = nil
			}
			for id, selected := range byScope {
				copy := cloneObject(scope)
				copy[recordKey] = encode(selected)
				byResource[id] = append(byResource[id], copy)
			}
		}
		if len(scopes) == 0 {
			byResource[unattributed] = nil
		}
		for id, selected := range byResource {
			copy := cloneObject(resource)
			copy[scopeKey] = encode(selected)
			grouped[id] = append(grouped[id], copy)
		}
	}
	if len(grouped) == 0 {
		return map[string][]byte{unattributed: body}, nil
	}
	if len(grouped) == 1 {
		for id := range grouped {
			return map[string][]byte{id: body}, nil
		}
	}
	result := make(map[string][]byte, len(grouped))
	for id, selected := range grouped {
		copy := cloneObject(top)
		copy[resourceKey] = encode(selected)
		result[id] = encode(copy)
	}
	return result, nil
}
