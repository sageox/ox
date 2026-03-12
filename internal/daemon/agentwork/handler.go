package agentwork

// WorkHandler defines how a specific work type is detected, prompted, and
// post-processed. Each handler is responsible for a single WorkItem.Type.
type WorkHandler interface {
	// Type returns the work item type string this handler manages
	// (e.g. "session-finalize").
	Type() string

	// Detect scans a ledger path and returns zero or more work items that
	// need processing. Returning an empty slice is not an error.
	Detect(ledgerPath string) ([]*WorkItem, error)

	// BuildPrompt converts a work item into a RunRequest suitable for the
	// Runner. The handler decides prompt text, working directory, token
	// budget, and timeout.
	BuildPrompt(item *WorkItem) (RunRequest, error)

	// ProcessResult handles the agent's output after a successful run.
	// Implementations typically persist derived artifacts back to the ledger.
	ProcessResult(item *WorkItem, result *RunResult) error
}
