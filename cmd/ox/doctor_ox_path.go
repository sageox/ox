package main

import (
	"context"

	"github.com/sageox/ox/internal/doctor/checks"
)

// CheckSlugOxInPath is the slug for the ox-in-PATH check.
const CheckSlugOxInPath = "ox-in-path"

const oxInPathCheckName = "ox in PATH"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:     CheckSlugOxInPath,
		Name:     oxInPathCheckName,
		Category: "Ecosystem",
		FixLevel: FixLevelSuggested,
		Description: "Detects whether ox is reachable from the non-interactive shell AI " +
			"coworker hooks actually run in -- not just whatever shell launched " +
			"`ox doctor`. Never writes to a shell rc file; only prints the exact line to add.",
		Run: func(fix bool) checkResult {
			return convertDoctorResult(checks.NewOxInPathCheck(nil).Run(context.Background(), fix))
		},
	})
}
