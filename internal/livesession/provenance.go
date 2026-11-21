package livesession

import "context"

// VerifyProvenance reports whether this machine may act on the stream state st
// read from the session folder dir: st names cfg's own endpoint and a
// conversation minted here for that folder (see authorize). A refusal is
// ErrUntrustedState, logged once at Error; an unreadable registry is returned
// as is.
//
// Every signed request already passes this check. It is exported for work
// that runs before any request and has a cost of its own: the daemon writes a
// close's summary with the person's model CLI before Close authorizes, and a
// stream-state.json force-added to the Ledger must never get to spend that.
func VerifyProvenance(ctx context.Context, cfg Config, dir string, st State) error {
	// the registry path and the refusal log come from the defaults
	cfg, err := cfg.withDefaults()
	if err != nil {
		return err
	}
	return authorize(ctx, cfg, dir, st)
}
