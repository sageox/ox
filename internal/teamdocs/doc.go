package teamdocs

// TeamDoc represents a single team document discovered from the docs/ directory
// within a team context. These docs are cataloged during ox agent prime so agents
// know what's available and when to read each document on demand.
type TeamDoc struct {
	Name        string `json:"name"`                  // filename (e.g., "principles.md")
	Title       string `json:"title"`                 // from frontmatter or first H1 heading
	Description string `json:"description,omitempty"` // one-line summary for catalog
	Path        string `json:"path"`                  // absolute path — agent reads on demand
	When        string `json:"when,omitempty"`         // natural language triggers for when to read
	Visibility  string `json:"visibility"`            // always | indexed | hidden
}

// Visibility levels control how docs are disclosed to agents during prime.
//
// Progressive disclosure model (inspired by Agent Skills spec):
//   - "always"  — content auto-inlined into prime output. Reserved for future use;
//     intended for small, universally-needed docs (<200 tokens) like glossaries.
//     Not yet implemented — accepted as a valid value but treated as "indexed" for now.
//   - "indexed" — listed in catalog with title + description + when triggers.
//     Agent reads full content on demand when task matches. This is the default.
//   - "hidden"  — not visible to agents at all. Use for human-only docs, drafts,
//     meeting notes, or directory instructions (like README.md).
const (
	VisibilityAlways  = "always"
	VisibilityIndexed = "indexed"
	VisibilityHidden  = "hidden"
)

// DefaultVisibility is the visibility applied when frontmatter omits the field.
// Docs in team context docs/ are visible by default — the whole point of the
// directory is sharing knowledge with the team, including AI coworkers.
const DefaultVisibility = VisibilityIndexed
