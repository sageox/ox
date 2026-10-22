package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sageox/ox/internal/secretguard"
)

const (
	feedbackMaxReportBytes = 4 << 10
	feedbackMaxReportChars = 4000
	feedbackMinTitleChars  = 8
	feedbackMaxTitleChars  = 200
)

var feedbackLoadLatestRules = secretguard.LatestRules

// readFeedbackReport bounds input before allocating or inspecting its contents.
func readFeedbackReport(reader io.Reader) (string, error) {
	content, err := io.ReadAll(io.LimitReader(reader, feedbackMaxReportBytes+1))
	if err != nil {
		return "", errors.New("cannot read feedback report")
	}
	if len(content) > feedbackMaxReportBytes {
		return "", errors.New("feedback report exceeds 4 KiB")
	}
	if !utf8.Valid(content) {
		return "", errors.New("feedback report must contain valid UTF-8")
	}
	for _, character := range string(content) {
		if unicode.IsControl(character) && character != '\t' && character != '\n' && character != '\r' {
			return "", errors.New("feedback report contains unsupported control characters")
		}
	}
	report := strings.TrimSpace(string(content))
	if report == "" {
		return "", errors.New("feedback report is empty; provide a title and description on stdin")
	}
	// The server counts JavaScript UTF-16 units, so supplementary characters
	// must count twice even though each is one Go rune.
	if feedbackUTF16Length(report) > feedbackMaxReportChars {
		return "", errors.New("feedback report exceeds 4000 characters")
	}
	title, _, _ := strings.Cut(report, "\n")
	title = strings.TrimSpace(title)
	titleLength := utf8.RuneCountInString(title)
	if titleLength < feedbackMinTitleChars || titleLength > feedbackMaxTitleChars {
		return "", fmt.Errorf("feedback title on the first line must be %d–%d characters", feedbackMinTitleChars, feedbackMaxTitleChars)
	}
	return report, nil
}

func feedbackUTF16Length(value string) int {
	length := 0
	for _, character := range value {
		length++
		if character > 0xffff {
			length++
		}
	}
	return length
}

func validateFeedbackType(value string) error {
	switch value {
	case "bug", "feature-request", "feedback":
		return nil
	default:
		return errors.New("feedback type must be bug, feature-request, or feedback")
	}
}

func validateFeedbackEmail(value string) error {
	if value == "" {
		return nil
	}
	address, err := mail.ParseAddress(value)
	_, domain, _ := strings.Cut(value, "@")
	if len(value) > 254 || err != nil || address.Name != "" || address.Address != value || strings.Count(value, "@") != 1 || !strings.Contains(domain, ".") || strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return errors.New("feedback email must be a plain email address of at most 254 characters")
	}
	return nil
}

func validateFeedbackLocalPayload(payload []byte) error {
	if err := secretguard.ValidateContent("feedback.json", payload); err != nil {
		return errors.New("feedback contains a possible secret; remove credentials and retry")
	}
	return nil
}

// validateFeedbackPayload checks the exact publication bytes against bundled
// and current rules. The canonical guard decodes JSON strings before scanning.
func validateFeedbackPayload(ctx context.Context, payload []byte) error {
	if err := validateFeedbackLocalPayload(payload); err != nil {
		return err
	}
	rules, err := feedbackLoadLatestRules(ctx)
	if err != nil {
		return errors.New("cannot verify current secret detection rules; feedback was not submitted")
	}
	if err := rules.ValidateContent("feedback.json", payload); err != nil {
		return errors.New("feedback contains a possible secret; remove credentials and retry")
	}
	return nil
}
