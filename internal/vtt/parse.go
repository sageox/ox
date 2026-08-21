package vtt

import (
	"fmt"
	"strings"
	"time"
)

// Cue represents a single WebVTT cue with optional speaker attribution.
type Cue struct {
	Speaker string        // e.g., "Speaker 1" or empty
	Text    string        // cue text content
	Index   int           // 1-based ordinal within the parsed file
	Start   time.Duration // media-clock start; zero when the timestamp was malformed
	End     time.Duration // media-clock end (exclusive); zero when the timestamp was malformed
}

// Parse extracts cues from WebVTT data.
// Handles the WEBVTT header, timestamp lines, and <v Speaker N> voice tags.
// Malformed cues are skipped; returns what can be parsed.
func Parse(data []byte) ([]Cue, error) {
	content := string(data)
	lines := strings.Split(content, "\n")

	if len(lines) == 0 || !strings.HasPrefix(strings.TrimSpace(lines[0]), "WEBVTT") {
		return nil, fmt.Errorf("not a WebVTT file: missing WEBVTT header")
	}

	var cues []Cue
	var currentText []string
	var curStart, curEnd time.Duration
	inCue := false

	emit := func() {
		cue := parseCueText(currentText)
		cue.Index = len(cues) + 1
		cue.Start = curStart
		cue.End = curEnd
		cues = append(cues, cue)
	}

	for i := 1; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")

		// blank line ends a cue. A body-less cue (timestamp line with no
		// text) still emits, with its parsed interval and empty text: cue
		// ordinals are the addressing key for cue-range selectors and
		// cue_ref citations, so Index must track true file position — a
		// silently dropped cue would shift every later cue's ordinal.
		if strings.TrimSpace(line) == "" {
			if inCue {
				emit()
				currentText = nil
			}
			inCue = false
			continue
		}

		// timestamp line starts a cue (contains "-->")
		if strings.Contains(line, "-->") {
			inCue = true
			currentText = nil
			// Malformed timestamps leave a zero (empty) interval: the cue
			// stays cue-addressable but never matches a time window.
			curStart, curEnd = 0, 0
			if s, e, err := parseTimestampPair(line); err == nil {
				curStart, curEnd = s, e
			}
			continue
		}

		// skip cue identifiers (numeric lines before timestamps)
		if !inCue {
			continue
		}

		// accumulate cue text
		currentText = append(currentText, line)
	}

	// flush last cue if file doesn't end with blank line (body-less
	// included — see the blank-line handler above)
	if inCue {
		emit()
	}

	return cues, nil
}

// parseCueText extracts speaker and text from cue content lines.
// Handles WebVTT voice tags like: <v Speaker 1>Hello world</v>
func parseCueText(lines []string) Cue {
	var speaker string
	var textParts []string

	for _, line := range lines {
		s, text := extractVoiceTag(line)
		if s != "" && speaker == "" {
			speaker = s
		}
		textParts = append(textParts, text)
	}

	return Cue{
		Speaker: speaker,
		Text:    strings.Join(textParts, " "),
	}
}

// extractVoiceTag parses a WebVTT voice tag from a line.
// Input: "<v Speaker 1>Hello world</v>" → ("Speaker 1", "Hello world")
// Input: "Hello world" → ("", "Hello world")
func extractVoiceTag(line string) (speaker, text string) {
	line = strings.TrimSpace(line)

	if !strings.HasPrefix(line, "<v ") {
		return "", stripTags(line)
	}

	// find closing > of voice tag
	closeIdx := strings.Index(line, ">")
	if closeIdx < 0 {
		return "", stripTags(line)
	}

	speaker = line[3:closeIdx]
	rest := line[closeIdx+1:]

	// strip closing </v> if present
	rest = strings.TrimSuffix(rest, "</v>")
	return speaker, strings.TrimSpace(rest)
}

// stripTags removes any remaining HTML-like tags from text.
func stripTags(s string) string {
	var result strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			result.WriteRune(r)
		}
	}
	return result.String()
}

// UniqueSpeakers returns the distinct speaker names from cues, in order of first appearance.
// Cues with empty speakers are skipped.
func UniqueSpeakers(cues []Cue) []string {
	seen := make(map[string]bool)
	var speakers []string
	for _, c := range cues {
		if c.Speaker != "" && !seen[c.Speaker] {
			seen[c.Speaker] = true
			speakers = append(speakers, c.Speaker)
		}
	}
	return speakers
}

// FormatAsText produces a readable "Speaker: text" format suitable for LLM consumption.
// Adjacent cues from the same speaker are merged into a single line.
func FormatAsText(cues []Cue) string {
	if len(cues) == 0 {
		return ""
	}

	var sb strings.Builder
	prevSpeaker := ""

	for _, cue := range cues {
		if cue.Text == "" {
			continue
		}

		if cue.Speaker == prevSpeaker && cue.Speaker != "" {
			// same speaker continues — append to previous line
			sb.WriteString(" ")
			sb.WriteString(cue.Text)
		} else {
			// new speaker or no speaker attribution
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			if cue.Speaker != "" {
				sb.WriteString(cue.Speaker)
				sb.WriteString(": ")
			}
			sb.WriteString(cue.Text)
			prevSpeaker = cue.Speaker
		}
	}

	return sb.String()
}
