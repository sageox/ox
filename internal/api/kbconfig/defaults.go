package kbconfig

// DefaultSessionRecordingMode returns the per-KB-type default recording mode.
// Mirrors mono's packages/kb/config.go DefaultSessionRecordingMode byte-for-byte.
//
// Work surfaces default to auto (personal, team, repo) — these are the
// bubbles people actively work *in* and recording is the value.
//
// Presence surfaces default to manual (profile, custom, channel) — these are
// curated public/broadcast bubbles where silent auto-recording would
// surprise contributors.
//
// Unknown / forward-compat KB types fall through to manual: when in doubt,
// the privacy-safer choice is to require an explicit opt-in to recording.
//
// Changing a default here flips behavior for every bubble that omits the
// field. Don't move a type from manual to auto without a comms plan and a
// one-shot migration that stamps the previous default into still-unset rows.
func DefaultSessionRecordingMode(kbType string) string {
	switch kbType {
	case KBTypePersonal, KBTypeTeam, KBTypeRepo:
		return SessionRecordingAuto
	case KBTypeProfile, KBTypeCustom, KBTypeChannel:
		return SessionRecordingManual
	}
	return SessionRecordingManual
}
