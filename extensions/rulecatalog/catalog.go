// Package rulecatalog contains ox's built-in coworker rules.
//
// Rules are plain catalog entries. Adapters describe where their native rule
// roots live; the central inventory planner owns projection, drift repair, and
// retirement. Keeping content here means an adapter never becomes a second
// installer or a second source of truth.
package rulecatalog

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// File is one repository-relative file beneath a declared rule target.
type File struct {
	Path    string
	Content []byte
}

const (
	PrimaryDescription     = "SageOx behavioral guidance for AI coworkers"
	TeamContextDescription = "How to discover and use team-context rules and knowledge from the SageOx ox CLI"
)

// LegacyDescriptions are the generated frontmatter descriptions used by the
// pre-inventory adapter installers. A verifying stamp plus one of these exact
// preambles is enough to migrate ownership without maintaining filename lists.
func LegacyDescriptions() []string {
	return []string{PrimaryDescription, TeamContextDescription}
}

//go:embed ox-cli.md
var embeddedPrimaryRule []byte

//go:embed ox-cli-use-team-context.md
var embeddedTeamContextRule []byte

// The catalog is embedded from the checkout at BUILD time, and git on Windows
// checks .md files out with CRLF by default. Normalizing once here — rather
// than at each use — is what makes it impossible for Select and Digest to
// disagree about the same source bytes. A Windows-built ox would otherwise
// WRITE CRLF rules and record their CRLF digests in the tracked skills
// lockfile, so a teammate's Linux build reads them as drift, rewrites them,
// and the two builds rewrite each other forever. The .gitattributes pin covers
// the same hazard but only for checkouts made after it landed.
var (
	primaryRule     = normalizeEOL(embeddedPrimaryRule)
	teamContextRule = normalizeEOL(embeddedTeamContextRule)
)

// normalizeEOL rewrites CRLF to LF. Deliberately a local copy of
// extensions/skills/catalog.go's normalizeEOL: the two catalogs are independent
// embed roots, and sharing five lines would couple them for no gain.
func normalizeEOL(b []byte) []byte {
	if !bytes.Contains(b, []byte("\r\n")) {
		return b
	}
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

// Select renders the built-in catalog for one native rule target.
func Select(target adapterprotocol.SkillTarget) ([]File, error) {
	if target.Format != adapterprotocol.RuleFormatMarkdownV1 {
		return nil, fmt.Errorf("unsupported rule target format %q", target.Format)
	}
	ruleRoot := strings.TrimSuffix(target.Root, "/") + "/"
	return []File{
		{Path: "ox-cli.md", Content: append([]byte(nil), primaryRule...)},
		{
			Path:    "ox-cli-use-team-context.md",
			Content: []byte(strings.ReplaceAll(string(teamContextRule), "{{RULE_ROOT}}", ruleRoot)),
		},
	}, nil
}

// Digest identifies the complete built-in rules source independent of target.
func Digest() (string, error) {
	files := []File{
		{Path: "ox-cli.md", Content: primaryRule},
		{Path: "ox-cli-use-team-context.md", Content: teamContextRule},
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	h := sha256.New()
	for _, file := range files {
		_, _ = h.Write([]byte(file.Path))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(file.Content)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
