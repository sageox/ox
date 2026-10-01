package plan

import "errors"

// TEMPORARY: replaced by feedback.go in integration.
//
// ErrDuplicateRound is returned by SaveFeedback, together with the path of the
// already-stored round, when a round with the same client-supplied ID was saved
// before (a browser retry after a lost response). The real definition and the
// dedupe logic land in feedback.go; this stub only lets the review server code
// against that contract in the meantime.
var ErrDuplicateRound = errors.New("duplicate feedback round")
