package materialize

import "github.com/sageox/ox/internal/trace/model"

var identities = map[string]bool{"user.email": true, "user.id": true, "user.account_id": true, "user.account_uuid": true, "organization.id": true}

type observations map[string]map[string]bool

func (o observations) observe(key string, value any) {
	if key != "app.version" && key != "app.entrypoint" && key != "terminal.type" {
		return
	}
	v, ok := value.(map[string]any)
	if !ok {
		return
	}
	s, ok := v["stringValue"].(string)
	if !ok || s == "" {
		return
	}
	if o[key] == nil {
		o[key] = map[string]bool{}
	}
	o[key][s] = true
}

func (o observations) unique(key string) *string {
	if len(o[key]) != 1 {
		return nil
	}
	for value := range o[key] {
		return &value
	}
	return nil
}

// scrub removes identity key/value attributes at every nesting level, including
// resource, scope, record, event, link, and OTLP AnyValue map/list attributes.
func scrub(value any, counts map[string]int64, seen observations) any {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if identities[key] {
				counts[key]++
				delete(v, key)
				continue
			}
			v[key] = scrub(child, counts, seen)
		}
		return v
	case []any:
		kept := v[:0]
		for _, child := range v {
			if attribute, ok := child.(map[string]any); ok {
				if key, ok := attribute["key"].(string); ok {
					if identities[key] {
						counts[key]++
						continue
					}
					seen.observe(key, attribute["value"])
				}
			}
			kept = append(kept, scrub(child, counts, seen))
		}
		return kept
	default:
		return value
	}
}

func countRecords(top map[string]any, signal string) int64 {
	resourceKey, scopeKey, recordKey := "resourceSpans", "scopeSpans", "spans"
	if signal == "events" {
		resourceKey, scopeKey, recordKey = "resourceLogs", "scopeLogs", "logRecords"
	}
	var total int64
	resources, _ := top[resourceKey].([]any)
	for _, resource := range resources {
		r, _ := resource.(map[string]any)
		scopes, _ := r[scopeKey].([]any)
		for _, scope := range scopes {
			s, _ := scope.(map[string]any)
			records, _ := s[recordKey].([]any)
			total += int64(len(records))
		}
	}
	return total
}

func applyObservations(meta *model.Metadata, seen observations) {
	meta.ClaudeCodeVersion = seen.unique("app.version")
	meta.Entrypoint = seen.unique("app.entrypoint")
	meta.TerminalType = seen.unique("terminal.type")
}
