package search

import (
	"fmt"
	"strconv"
	"strings"
)

// tokenize splits input into tokens, respecting quoted strings.
func tokenize(input string) []string {
	var tokens []string
	var current strings.Builder
	runes := []rune(input)
	i := 0

	for i < len(runes) {
		ch := runes[i]
		switch ch {
		case '"':
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
			i++
			var quoted strings.Builder
			for i < len(runes) && runes[i] != '"' {
				quoted.WriteRune(runes[i])
				i++
			}
			if i < len(runes) {
				i++ // skip closing quote
			}
			tokens = append(tokens, `"`+quoted.String()+`"`)
		case ' ', '\t':
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
			i++
		default:
			current.WriteRune(ch)
			i++
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

func parseSelect(value string) (SelectType, string, error) {
	switch value {
	case "repo":
		return SelectRepo, "", nil
	case "file":
		return SelectFile, "", nil
	case "symbol":
		return SelectSymbol, "", nil
	default:
		if strings.HasPrefix(value, "symbol.") {
			kind := value[len("symbol."):]
			return SelectSymbolKind, kind, nil
		}
		return SelectNone, "", fmt.Errorf("unknown select type '%s'. Valid: repo, file, symbol, symbol.<kind>", value)
	}
}

// ParseQuery parses a query string.
func ParseQuery(input string) (*ParsedQuery, error) {
	tokens := tokenize(input)
	var filters Filters
	searchType := SearchTypeCode
	var searchTerms []string
	forceRegex := false

	for _, token := range tokens {
		negated := false
		rest := token
		if strings.HasPrefix(rest, "-") {
			negated = true
			rest = rest[1:]
		}

		if idx := strings.Index(rest, ":"); idx >= 0 {
			key := rest[:idx]
			value := rest[idx+1:]

			switch {
			case !negated && key == "repo":
				if atIdx := strings.Index(value, "@"); atIdx >= 0 {
					filters.Repo = value[:atIdx]
					filters.Rev = value[atIdx+1:]
				} else {
					filters.Repo = value
				}
			case negated && key == "repo":
				filters.NegRepo = value
			case !negated && (key == "file"):
				filters.File = value
			case negated && key == "file":
				filters.NegFile = value
			case !negated && (key == "lang" || key == "language" || key == "l"):
				filters.Lang = value
			case negated && (key == "lang" || key == "language" || key == "l"):
				filters.NegLang = value
			case !negated && key == "type":
				switch value {
				case "code":
					searchType = SearchTypeCode
				case "diff":
					searchType = SearchTypeDiff
				case "commit":
					searchType = SearchTypeCommit
				case "symbol":
					searchType = SearchTypeSymbol
				case "comment":
					searchType = SearchTypeComment
				default:
					return nil, fmt.Errorf("unknown search type '%s'. Valid types: code, symbol, diff, commit, comment", value)
				}
			case !negated && (key == "rev" || key == "revision"):
				filters.Rev = value
			case !negated && key == "count":
				n, err := strconv.Atoi(value)
				if err != nil || n <= 0 {
					return nil, fmt.Errorf("count: must be a positive integer, got '%s'", value)
				}
				filters.Count = n
			case !negated && key == "case":
				filters.Case = value == "yes"
			case !negated && key == "patterntype":
				switch value {
				case "literal", "keyword":
					// default
				case "regexp":
					forceRegex = true
				default:
					return nil, fmt.Errorf("unsupported pattern type '%s'. Valid: literal, keyword, regexp", value)
				}
			case !negated && key == "author":
				filters.Author = value
			case negated && key == "author":
				filters.NegAuthor = value
			case !negated && key == "before":
				filters.Before = value
			case !negated && key == "after":
				filters.After = value
			case !negated && key == "message":
				filters.Message = value
			case negated && key == "message":
				filters.NegMessage = value
			case !negated && key == "select":
				st, kind, err := parseSelect(value)
				if err != nil {
					return nil, err
				}
				filters.Select = st
				filters.SelectKind = kind
			case !negated && key == "calls":
				filters.Calls = value
			case !negated && key == "calledby":
				filters.CalledBy = value
			case !negated && key == "returns":
				filters.Returns = value
			case !negated && (key == "ckind" || key == "comment-kind"):
				filters.CommentKind = value
			case negated:
				return nil, fmt.Errorf("negation not supported for '%s:'", key)
			default:
				// Not a known filter — treat as search term
				searchTerms = append(searchTerms, token)
			}
		} else if token == "OR" {
			searchTerms = append(searchTerms, "OR")
		} else {
			term := strings.Trim(token, `"`)
			searchTerms = append(searchTerms, term)
		}
	}

	// Split by OR into groups
	var orGroups []string
	var currentGroup []string
	for _, term := range searchTerms {
		if term == "OR" {
			if len(currentGroup) > 0 {
				orGroups = append(orGroups, strings.Join(currentGroup, " "))
				currentGroup = nil
			}
		} else {
			currentGroup = append(currentGroup, term)
		}
	}
	if len(currentGroup) > 0 {
		orGroups = append(orGroups, strings.Join(currentGroup, " "))
	}

	// Detect /regex/ pattern
	isRegex := forceRegex
	if !isRegex && len(orGroups) == 1 {
		p := orGroups[0]
		if len(p) >= 2 && strings.HasPrefix(p, "/") && strings.HasSuffix(p, "/") {
			isRegex = true
			orGroups[0] = p[1 : len(p)-1]
		}
	}

	if isRegex && len(orGroups) > 0 {
		allEmpty := true
		for _, g := range orGroups {
			if g != "" {
				allEmpty = false
				break
			}
		}
		if allEmpty {
			return nil, fmt.Errorf("empty regex pattern")
		}
		if len(orGroups) > 1 {
			return nil, fmt.Errorf("regex patterns cannot be combined with OR")
		}
	}

	return &ParsedQuery{
		SearchTerms: orGroups,
		Type:        searchType,
		IsRegex:     isRegex,
		Filters:     filters,
	}, nil
}
