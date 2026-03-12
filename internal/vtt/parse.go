package vtt

import (
	"fmt"
	"strings"
)

// Cue represents a single WebVTT cue with optional speaker attribution.
type Cue struct {
	Speaker string // e.g., "Speaker 1" or empty
	Text    string // cue text content
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
	inCue := false

	for i := 1; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")

		// blank line ends a cue
		if strings.TrimSpace(line) == "" {
			if inCue && len(currentText) > 0 {
				cues = append(cues, parseCueText(currentText))
				currentText = nil
			}
			inCue = false
			continue
		}

		// timestamp line starts a cue (contains "-->")
		if strings.Contains(line, "-->") {
			inCue = true
			currentText = nil
			continue
		}

		// skip cue identifiers (numeric lines before timestamps)
		if !inCue {
			continue
		}

		// accumulate cue text
		currentText = append(currentText, line)
	}

	// flush last cue if file doesn't end with blank line
	if inCue && len(currentText) > 0 {
		cues = append(cues, parseCueText(currentText))
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
