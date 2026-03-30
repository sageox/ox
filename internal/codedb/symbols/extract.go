package symbols

import (
	"sort"
	"strings"
	"sync"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// langToExt maps language names to fake filenames for DetectLanguage.
var langToExt = map[string]string{
	"go":         "x.go",
	"python":     "x.py",
	"javascript": "x.js",
	"typescript":  "x.ts",
	"tsx":         "x.tsx",
	"rust":        "x.rs",
	"c":           "x.c",
	"cpp":         "x.cpp",
	"c++":         "x.cpp",
	"java":        "x.java",
	"ruby":        "x.rb",
	"php":         "x.php",
}

// supportedLangs is the canonical list returned by SupportedLanguages.
var supportedLangs = []string{
	"go", "python", "javascript", "typescript", "tsx", "rust", "c", "cpp",
}

// langCache holds pre-resolved language entries and taggers.
// Populated once via initLangs to avoid data races in
// grammars.DetectLanguage (upstream resolveHighlightInheritance is not thread-safe).
type langEntry struct {
	entry     grammars.LangEntry
	tagsQuery string
	lang      *gotreesitter.Language
}

var (
	langInitOnce sync.Once
	langCache    map[string]*langEntry // keyed by lowercase language name
)

func initLangs() {
	langInitOnce.Do(func() {
		langCache = make(map[string]*langEntry, len(langToExt))
		for lang, fakeFile := range langToExt {
			entry := grammars.DetectLanguage(fakeFile)
			if entry == nil {
				continue
			}
			tq := grammars.ResolveTagsQuery(*entry)
			if tq == "" {
				continue
			}
			l := entry.Language()
			if l == nil {
				continue
			}
			langCache[lang] = &langEntry{
				entry:     *entry,
				tagsQuery: tq,
				lang:      l,
			}
		}
	})
}

// Extract parses source code in the given language and returns symbol
// definitions and references using tree-sitter tags queries.
func Extract(source, language string) ([]Symbol, []Ref) {
	if source == "" {
		return nil, nil
	}

	initLangs()

	language = strings.ToLower(language)
	if language == "c++" {
		language = "cpp"
	}
	le, ok := langCache[language]
	if !ok {
		return nil, nil
	}

	tagger, err := gotreesitter.NewTagger(le.lang, le.tagsQuery)
	if err != nil {
		return nil, nil
	}

	tags := tagger.Tag([]byte(source))
	if len(tags) == 0 {
		return nil, nil
	}

	var syms []Symbol
	var refs []Ref
	var refStartBytes []uint32

	for _, tag := range tags {
		switch {
		case strings.HasPrefix(tag.Kind, "definition."):
			kind := strings.TrimPrefix(tag.Kind, "definition.")
			syms = append(syms, Symbol{
				Name:      tag.Name,
				Kind:      kind,
				Line:      int(tag.NameRange.StartPoint.Row) + 1,
				Col:       int(tag.NameRange.StartPoint.Column) + 1,
				EndLine:   int(tag.Range.EndPoint.Row) + 1,
				EndCol:    int(tag.Range.EndPoint.Column) + 1,
				ParentIdx: -1,
				startByte: tag.Range.StartByte,
				endByte:   tag.Range.EndByte,
			})

		case strings.HasPrefix(tag.Kind, "reference."):
			kind := strings.TrimPrefix(tag.Kind, "reference.")
			refs = append(refs, Ref{
				RefName:          tag.Name,
				Kind:             kind,
				Line:             int(tag.NameRange.StartPoint.Row) + 1,
				Col:              int(tag.NameRange.StartPoint.Column) + 1,
				ContainingSymIdx: -1,
			})
			refStartBytes = append(refStartBytes, tag.NameRange.StartByte)
		}
	}

	assignParents(syms)
	assignContainingSymbols(syms, refs, refStartBytes)
	extractTypeInfo(le.lang, language, []byte(source), syms)

	return syms, refs
}

// SupportedLanguages returns the list of languages supported by Extract.
func SupportedLanguages() []string {
	out := make([]string, len(supportedLangs))
	copy(out, supportedLangs)
	return out
}

// assignParents sets ParentIdx for nested symbols using a stack-based
// approach. Symbols are sorted by (startByte ASC, endByte DESC) so that
// parents always appear before their children.
func assignParents(syms []Symbol) {
	if len(syms) == 0 {
		return
	}

	// build index mapping to preserve original positions after sort
	indices := make([]int, len(syms))
	for i := range indices {
		indices[i] = i
	}
	sort.Slice(indices, func(a, b int) bool {
		ia, ib := indices[a], indices[b]
		if syms[ia].startByte != syms[ib].startByte {
			return syms[ia].startByte < syms[ib].startByte
		}
		return syms[ia].endByte > syms[ib].endByte
	})

	type stackEntry struct {
		idx     int    // original index in syms
		endByte uint32
	}
	var stack []stackEntry

	for _, origIdx := range indices {
		sym := &syms[origIdx]

		// pop entries that ended before this symbol starts
		for len(stack) > 0 && stack[len(stack)-1].endByte <= sym.startByte {
			stack = stack[:len(stack)-1]
		}

		// only assign parent if the stack top fully contains this symbol
		if len(stack) > 0 && stack[len(stack)-1].endByte >= sym.endByte {
			sym.ParentIdx = stack[len(stack)-1].idx
		}

		stack = append(stack, stackEntry{idx: origIdx, endByte: sym.endByte})
	}
}

// assignContainingSymbols finds the innermost symbol containing each ref
// (smallest byte span that encloses the ref's start byte).
// O(n*m) scan chosen for clarity; sufficient for typical file sizes.
// An interval tree could replace this if profiling shows it's a bottleneck.
func assignContainingSymbols(syms []Symbol, refs []Ref, refStartBytes []uint32) {
	if len(refs) == 0 || len(syms) == 0 {
		return
	}

	for i := range refs {
		rsb := refStartBytes[i]
		bestIdx := -1
		bestSpan := uint32(0)

		for j := range syms {
			if syms[j].startByte <= rsb && rsb < syms[j].endByte {
				span := syms[j].endByte - syms[j].startByte
				if bestIdx == -1 || span < bestSpan {
					bestIdx = j
					bestSpan = span
				}
			}
		}

		refs[i].ContainingSymIdx = bestIdx
	}
}
