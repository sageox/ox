package config

// RecordingReminder mode constants.
const (
	RecordingReminderOff = "off"
	RecordingReminderOn  = "on"
)

// NormalizeRecordingReminder returns the canonical recording reminder mode.
// Empty string and unrecognized values default to "on".
func NormalizeRecordingReminder(mode string) string {
	switch mode {
	case RecordingReminderOff:
		return RecordingReminderOff
	default:
		return RecordingReminderOn
	}
}

// ResolveRecordingReminder determines the effective recording reminder mode.
// Priority: User config > Project config > Default ("on").
// No caching — called infrequently by daemon whisper source.
func ResolveRecordingReminder(projectRoot string) string {
	userCfg, _ := LoadUserConfig()
	if userCfg != nil && userCfg.RecordingReminder != "" {
		return NormalizeRecordingReminder(userCfg.RecordingReminder)
	}
	if projectRoot != "" {
		cfg, _ := LoadProjectConfig(projectRoot)
		if cfg != nil && cfg.RecordingReminder != "" {
			return NormalizeRecordingReminder(cfg.RecordingReminder)
		}
	}
	return RecordingReminderOn
}

// RecordingReminderEnabled returns true if recording reminders are active.
func RecordingReminderEnabled(projectRoot string) bool {
	return ResolveRecordingReminder(projectRoot) == RecordingReminderOn
}
