package app

// Section identifies which tab section is active in the dashboard.
type Section int

const (
	SectionOverview Section = iota // system health at a glance
	SectionSync                    // ledger + team context sync
	SectionCode                    // code indexing status
	SectionSessions                // session recordings + AI coworkers
	SectionFeed                    // murmurs, discussions, whispers
	sectionCount                   // sentinel
)

// Label returns the display name for the section tab.
func (s Section) Label() string {
	switch s {
	case SectionOverview:
		return "Overview"
	case SectionSync:
		return "Sync"
	case SectionCode:
		return "Code"
	case SectionSessions:
		return "Sessions"
	case SectionFeed:
		return "Feed"
	default:
		return "?"
	}
}

// Key returns the number key shortcut (1-5).
func (s Section) Key() string {
	return string(rune('1' + int(s)))
}

// NextSection returns the section after s, wrapping around.
func NextSection(s Section) Section {
	return Section((int(s) + 1) % int(sectionCount))
}

// PrevSection returns the section before s, wrapping around.
func PrevSection(s Section) Section {
	return Section((int(s) + int(sectionCount) - 1) % int(sectionCount))
}
