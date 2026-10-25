package nativeimport

import (
	"html"
	"regexp"
	"strings"
)

// Marker is ox's own <session-context> tag, printed by `ox agent prime` at the
// start of every session ox recorded since 2026-03-12 and kept verbatim in the
// native transcript. It identifies the recording without any stored native ID.
type Marker struct {
	AgentID string // the ox agent instance, Ox plus four characters
	URL     string // the recording's link, present while recording
}

var (
	markerTag     = regexp.MustCompile(`<session-context\b([^>]*)>`)
	markerAttr    = regexp.MustCompile(`(\w+)="([^"]*)"`)
	agentIDFormat = regexp.MustCompile(`^Ox[0-9A-Za-z]{4}$`)
	urlSessionID  = regexp.MustCompile(`/c/(ses_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})(?:[/?#]|$)`)
	urlSession    = regexp.MustCompile(`/sessions/([^/?#"]+)/view(?:[/?#]|$)`)
)

// SessionID is the recording's ses_ ID when the marker links to /c/ses_<id>
// (ox 2026-05-01 and later).
func (m Marker) SessionID() string {
	if match := urlSessionID.FindStringSubmatch(m.URL); match != nil {
		return match[1]
	}
	return ""
}

// SessionName is the recording's Ledger directory when the marker links to
// /repo/<id>/sessions/<name>/view (ox 2026-03-12 to 2026-05-01).
func (m Marker) SessionName() string {
	if match := urlSession.FindStringSubmatch(m.URL); match != nil {
		return match[1]
	}
	return ""
}

// ParseMarkers returns every well-formed session-context tag in text.
func ParseMarkers(text string) []Marker {
	if !strings.Contains(text, "<session-context") {
		return nil
	}
	var out []Marker
	for _, tag := range markerTag.FindAllStringSubmatch(text, -1) {
		attrs := map[string]string{}
		for _, attr := range markerAttr.FindAllStringSubmatch(tag[1], -1) {
			attrs[attr[1]] = html.UnescapeString(attr[2])
		}
		if !agentIDFormat.MatchString(attrs["agent_id"]) {
			continue
		}
		out = append(out, Marker{
			AgentID: attrs["agent_id"],
			URL:     attrs["url"],
		})
	}
	return out
}

// WalkStrings walks decoded JSON and hands every string to fn. Hook output
// and tool results nest their text at different depths across versions.
func WalkStrings(value any, fn func(string)) {
	switch v := value.(type) {
	case string:
		fn(v)
	case map[string]any:
		for _, item := range v {
			WalkStrings(item, fn)
		}
	case []any:
		for _, item := range v {
			WalkStrings(item, fn)
		}
	}
}

func markersIn(value any) []Marker {
	var out []Marker
	WalkStrings(value, func(s string) { out = append(out, ParseMarkers(s)...) })
	return out
}
