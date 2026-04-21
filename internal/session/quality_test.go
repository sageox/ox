package session

import "testing"

func TestEvaluateQuality(t *testing.T) {
	tests := []struct {
		name    string
		score   float64
		upload  float64
		discard float64
		want    QualityDisposition
	}{
		// Regression for #525: a real LLM score of 0 (e.g. empty session
		// correctly scored 0) must flow through the discard gate. The function
		// no longer has an "unscored sentinel" branch — "unscored" decisions
		// are the caller's responsibility.
		{"explicit zero is discarded below discard threshold", 0.0, 0.3, 0.1, QualityDiscard},
		{"below discard threshold", 0.05, 0.3, 0.1, QualityDiscard},
		{"at discard threshold boundary", 0.1, 0.3, 0.1, QualityLocalOnly},
		{"between thresholds", 0.2, 0.3, 0.1, QualityLocalOnly},
		{"at upload threshold", 0.3, 0.3, 0.1, QualityUpload},
		{"above upload threshold", 0.8, 0.3, 0.1, QualityUpload},
		{"perfect score", 1.0, 0.3, 0.1, QualityUpload},
		{"custom high thresholds", 0.5, 0.7, 0.3, QualityLocalOnly},
		{"custom high thresholds above", 0.8, 0.7, 0.3, QualityUpload},
		{"custom high thresholds below", 0.2, 0.7, 0.3, QualityDiscard},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateQuality(tt.score, tt.upload, tt.discard)
			if got != tt.want {
				t.Errorf("EvaluateQuality(%f, %f, %f) = %q, want %q",
					tt.score, tt.upload, tt.discard, got, tt.want)
			}
		})
	}
}
