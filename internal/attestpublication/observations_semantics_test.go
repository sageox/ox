package attestpublication

import (
	"encoding/json"
	"errors"
)

// JSON Schema validates each count's type, but cannot compare aggregates with
// evidence. Every contract consumer must also apply this semantic check.
func validateObservationsSemantics(raw []byte) error {
	type counts struct {
		Referenced int `json:"referenced"`
		Available  int `json:"available"`
		Missing    int `json:"missing"`
		Invalid    int `json:"invalid"`
	}
	var observations struct {
		Evidence []struct {
			Status string `json:"status"`
		} `json:"evidence"`
		Counts counts `json:"counts"`
	}
	if err := json.Unmarshal(raw, &observations); err != nil {
		return err
	}
	expected := counts{Referenced: len(observations.Evidence)}
	for _, item := range observations.Evidence {
		switch item.Status {
		case "available":
			expected.Available++
		case "missing":
			expected.Missing++
		case "invalid":
			expected.Invalid++
		default:
			return errors.New("unsupported evidence status")
		}
	}
	if observations.Counts != expected {
		return errors.New("observations counts do not match evidence")
	}
	return nil
}
