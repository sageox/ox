# Tokens

Semantic color tokens are the unit of design composition in ox. Components reference tokens; tokens reference hex values. Hex values live in `internal/theme/generated.go` (synced from `sageox-design`).

## Brand & accent

| Token | Light | Dark | When to use |
|-------|-------|------|-------------|
| `ColorPrimary` | `#4F6A48` | `#7A8F78` | App name, headers, spinners — the calm sage anchor |
| `ColorSecondary` | `#B87D3A` | `#E0A56A` | Commands, interactive elements — copper highlight |
| `ColorAccent` | `#3D5437` | `#8FA888` | File paths, callouts, "look here" |

## Semantic

| Token | Light | Dark | When to use |
|-------|-------|------|-------------|
| `ColorSuccess` | `#4F6A48` | `#7A8F78` | Passed checks, confirmations |
| `ColorWarning` | `#B87D3A` | `#E0A56A` | Cautions, dirty builds, soft warnings |
| `ColorError` | `#A03030` | `#E07070` | Failures, blocked items, hard stops |
| `ColorInfo` | `#5580A0` | `#7FA7C8` | Flags, links, informational notes |
| `ColorDim` | `#6B7580` | `#8F99A3` | Descriptions, secondary text, "still here but quiet" |

## Visibility

| Token | Light | Dark | When to use |
|-------|-------|------|-------------|
| `ColorPublic` | `#0f766e` | `#2dd4bf` | "Visible to your team" — teal |
| `ColorPrivate` | `#b45309` | `#fbbf24` | "Personal / not shared" — amber |

## Wordmark (special)

| Token | Light | Dark | When to use |
|-------|-------|------|-------------|
| `ColorWordmarkSage` | `#7a8f78` | `#c4d1c0` | "Sage" half of the wordmark |
| `ColorWordmarkOx` | `#546a54` | `#7a8f78` | "Ox" half of the wordmark |

## Dashboard-specific tokens

`internal/dashboard/theme/tokens.go` defines an additional set of semantic-name constants (e.g., `TokenBorderFocused`, `TokenTimelineNow`, `TokenNavSelected`) used by the dashboard TUI. These are documentation-grade — they let dashboard styles refer to intent rather than color, paving the way for a user-configurable theme file later.

## Adding a token

1. Open a PR in [sageox-design](https://github.com/sageox/sageox-design/) modifying `tokens/colors.yaml` and `tokens/themes.yaml`.
2. After merge: `cd ../sageox-design && npm run sync` — regenerates `internal/theme/generated.go`.
3. Add a corresponding `Style…` in `internal/cli/styles.go` (semantic style wrapping the new color).
4. Update this file with the new row.

Never define a token in `internal/theme/generated.go` directly: it's overwritten on the next sync.
