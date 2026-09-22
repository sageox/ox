package teamconverge

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/addons"
	"github.com/stretchr/testify/require"
)

// lockOwning builds a lock that owns one add-on skill's files, in the shape the
// installer writes them.
func lockOwning(t *testing.T, addon, version string, paths ...string) addons.Lock {
	t.Helper()
	files := make([]addons.LockedFile, 0, len(paths))
	for _, p := range paths {
		files = append(files, addons.LockedFile{Path: p, Digest: "sha256:" + p, Mode: 0o644})
	}
	return addons.Lock{
		SchemaVersion: addons.LockSchemaVersion,
		Addons: []addons.LockedAddon{{
			Addon: addon, Source: addons.SourceBuiltin, Version: version,
			Digest: "sha256:addon", InstalledAt: time.Now().UTC(), Files: files,
		}},
	}
}

// TestOriginResolver_SkillDirectoryResolvesToItsAddon is the shape mismatch that
// would have made add-on delivery silently wrong.
//
// The lock records FILES ("agents/skills/deploy/SKILL.md"). Team Context
// discovery describes a skill by its DIRECTORY ("agents/skills/deploy"). An
// exact-match-only lookup answers OriginLoose for the directory — a perfectly
// valid-looking answer that reattributes every add-on-installed skill to the
// team, with nothing downstream to question it.
//
// Failure prevented: `ox sync` and `ox skills status` reporting add-on content
// as hand-authored, so nobody can tell what the catalog owns.
func TestOriginResolver_SkillDirectoryResolvesToItsAddon(t *testing.T) {
	lock := lockOwning(t, "post-cutoff", "1.0.0",
		"agents/skills/post-cutoff/SKILL.md",
		"agents/skills/post-cutoff/references/jev.md",
	)
	r := NewOriginResolver(lock)

	for _, tc := range []struct {
		name, sourcePath string
		wantKind         OriginKind
		wantAddon        string
	}{
		{"skill directory (what discovery passes)", "agents/skills/post-cutoff", OriginAddon, "post-cutoff"},
		{"a file inside it", "agents/skills/post-cutoff/SKILL.md", OriginAddon, "post-cutoff"},
		{"nested reference file", "agents/skills/post-cutoff/references/jev.md", OriginAddon, "post-cutoff"},
		{"hand-authored sibling directory", "agents/skills/deploy", OriginLoose, ""},
		// A prefix that is not a path boundary must NOT match: the lock owning
		// "agents/skills/post-cutoff/..." says nothing about "post-cutoff-notes".
		{"name-prefix neighbor is not owned", "agents/skills/post-cutoff-notes", OriginLoose, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Resolve(tc.sourcePath)
			require.Equal(t, tc.wantKind, got.Kind,
				"%q must resolve as %s", tc.sourcePath, tc.wantKind)
			require.Equal(t, tc.wantAddon, got.Addon)
			if tc.wantKind == OriginAddon {
				require.Equal(t, "1.0.0", got.AddonVersion, "provenance must carry the pinned version")
				require.NotEmpty(t, got.Digest, "provenance must carry the per-file digest")
			}
		})
	}
}

// TestValidateArtifact_AcceptsAddonOriginRejectsUnknown pins the convergence
// guard that would otherwise throw away everything the catalog installs.
//
// validateArtifact predates the Add-on Catalog, when OriginLoose was the only
// kind anything could produce. Left unchanged it rejects every add-on artifact
// as "unknown origin" — the feature installs content and convergence discards
// it. Widening it must NOT become an open door: a kind nobody defined is still
// a bug worth refusing.
func TestValidateArtifact_AcceptsAddonOriginRejectsUnknown(t *testing.T) {
	base := func(o Origin) Artifact {
		return Artifact{Kind: KindSkill, Name: "post-cutoff",
			SourcePath: "agents/skills/post-cutoff", Origin: o}
	}

	require.NoError(t, validateArtifact(base(Origin{Kind: OriginLoose})),
		"hand-authored artifacts must still validate")
	require.NoError(t, validateArtifact(base(Origin{
		Kind: OriginAddon, Addon: "post-cutoff", AddonVersion: "1.0.0", Digest: "sha256:x",
	})), "an add-on-installed artifact must validate, or convergence delivers nothing")

	err := validateArtifact(base(Origin{Kind: OriginKind("fabricated")}))
	require.Error(t, err, "an undefined origin kind must still be refused")
	require.Contains(t, err.Error(), "unknown origin")
}
