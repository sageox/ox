package session

import (
	"fmt"
	"io"
)

// ValidateJSONL validates JSONL input as a captured history.
// This is the primary entry point for validating agent-generated history.
//
// Returns the parsed and validated history on success.
// Returns an error if:
//   - Input is nil or empty
//   - First line is not a valid _meta object
//   - _meta is missing required fields (schema_version, source, agent_id)
//   - Entry type is not one of: user, assistant, system, tool
//   - Entry seq numbers are not monotonically increasing
//
// Example usage:
//
//	history, err := ValidateJSONL(reader)
//	if err != nil {
//	    return fmt.Errorf("invalid history: %w", err)
//	}
func ValidateJSONL(reader io.Reader) (*CapturedHistory, error) {
	return ValidateHistoryJSONLReader(reader)
}

// ValidateAndRedact validates JSONL input and applies secret redaction.
// This combines validation and security processing in a single call.
//
// Returns:
//   - Validated and redacted history
//   - Count of secrets redacted
//   - Error if validation fails
func ValidateAndRedact(reader io.Reader) (*CapturedHistory, int, error) {
	history, err := ValidateHistoryJSONLReader(reader)
	if err != nil {
		return nil, 0, err
	}

	redactor := NewRedactor()
	redactedCount := redactor.RedactCapturedHistory(history)

	return history, redactedCount, nil
}

// ValidateCapturePriorInput validates input specifically for capture-prior command.
// It differs from the schema in these ways:
//   - The _meta header may omit agent_id and source; CapturePrior fills them before storing
//   - At least one entry required
//   - Updates time_range in metadata if not set
//   - Sets message_count in metadata
func ValidateCapturePriorInput(reader io.Reader) (*CapturedHistory, error) {
	history, err := readHistoryJSONL(reader, validateCapturePriorMeta)
	if err != nil {
		return nil, err
	}

	if len(history.Entries) == 0 {
		return nil, fmt.Errorf("capture-prior requires at least one entry")
	}

	// compute and set time range if not already set
	if history.Meta != nil {
		if history.Meta.TimeRange == nil {
			history.Meta.TimeRange = history.ComputeTimeRange()
		}
		if history.Meta.MessageCount == 0 {
			history.Meta.MessageCount = len(history.Entries)
		}
	}

	return history, nil
}

// validateCapturePriorMeta checks a capture-prior header. Only schema_version is required:
// CapturePrior takes a missing agent_id from its command line and defaults source, so an AI
// coworker following an older or shorter example is not refused.
func validateCapturePriorMeta(meta *HistoryMeta) (*HistoryMeta, error) {
	if meta.SchemaVersion == "" {
		return nil, fmt.Errorf("%w: schema_version", ErrHistoryInvalidMeta)
	}
	return meta, nil
}
