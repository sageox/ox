package session

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/sageox/ox/internal/lfs"
)

// ErrLedgerSessionOverwrite marks a publish stopped before it touched anything because
// the Ledger entry it would write over cannot be proven to belong to this
// recording.
var ErrLedgerSessionOverwrite = errors.New("recovery refused to overwrite a Ledger session")

// CheckLedgerSessionDestination decides, before recovery mutates anything, whether
// the Ledger directory for sessionName may receive this recording.
// incomingID is the ID the recording carries: its state, else its raw header.
//
// Recovery keeps the cache directory's name as the Ledger key, and two sessions
// started in one minute can share it. A finalized session is the Ledger's record,
// so recovery may rewrite it only when both IDs are present and equal (a retry
// of an interrupted recovery). Every other finalized destination is refused.
// Tradeoff: an interrupted legacy retry is now refused rather than guessed;
// doctor surfaces it.
//
// Allowed: no meta.json yet, and a draft placeholder (it carries the ID the
// finished session keeps) unless it names a different session.
func CheckLedgerSessionDestination(ledgerSessionDir, sessionName, incomingID string) error {
	return checkLedgerSessionDestination(ledgerSessionDir, sessionName, incomingID, true)
}

// CheckFinalizedLedgerSessionDestination protects finalized entries during
// normal finalization, where the existing draft's ID can take precedence.
func CheckFinalizedLedgerSessionDestination(ledgerSessionDir, sessionName, incomingID string) error {
	return checkLedgerSessionDestination(ledgerSessionDir, sessionName, incomingID, false)
}

func checkLedgerSessionDestination(ledgerSessionDir, sessionName, incomingID string, checkDraft bool) error {
	// the name is the last element of a path in the recording state and becomes
	// a Ledger path that a draft purge RemoveAlls: "", "." and ".." would
	// address the Ledger or its sessions directory, not one session. (Unlike
	// validateDraftSessionName this allows ".." inside a name: a username can
	// carry it, and such a recovery must stay allowed.)
	if sessionName == "" || sessionName == "." || sessionName == ".." || strings.ContainsAny(sessionName, `/\`) {
		return fmt.Errorf("%w: cache directory name %q is not a single session name", ErrLedgerSessionOverwrite, sessionName)
	}

	existing, err := lfs.ReadSessionMeta(ledgerSessionDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		// meta.json arrives from teammates by git, and its parse errors can echo
		// its text back (a files key), so sanitize before it reaches a terminal
		return fmt.Errorf("%w: session %q has unreadable existing metadata: %+q", ErrLedgerSessionOverwrite, sessionName, err.Error())
	}

	destinationID := existing.SessionID
	if existing.IsDraft() {
		if checkDraft && destinationID != "" && incomingID != "" && destinationID != incomingID {
			return fmt.Errorf("%w: draft %q belongs to session ID %q, not the incoming session ID %q", ErrLedgerSessionOverwrite, sessionName, destinationID, incomingID)
		}
		return nil
	}

	switch {
	case destinationID == "":
		return fmt.Errorf("%w: finalized session %q has legacy metadata without a session ID", ErrLedgerSessionOverwrite, sessionName)
	case incomingID == "":
		return fmt.Errorf("%w: finalized session %q has session ID %q but the recording carries no session ID", ErrLedgerSessionOverwrite, sessionName, destinationID)
	case destinationID != incomingID:
		return fmt.Errorf("%w: finalized session %q has session ID %q, not the incoming session ID %q", ErrLedgerSessionOverwrite, sessionName, destinationID, incomingID)
	}
	return nil
}
