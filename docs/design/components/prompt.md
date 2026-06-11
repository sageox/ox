---
component: prompt
package: internal/cli
since: 0.3.0
family: input
renderer: freeze
exports: [Prompt]
---

# Prompt

> Free-form text input with a default suggestion.

## When to use

Anywhere you'd otherwise read stdin and parse manually. Reads from stdin in non-TTY mode without changing the contract — so `echo X | ox foo` composes cleanly.

## When NOT to use

Sensitive input (no echo masking yet — file an issue if needed). Structured data better served by flags. Lists of options (use [Select](select.md)).

## Anatomy

`ox dev catalog --component=prompt` or [sageox-design.netlify.app/catalog/cli/#c-prompt](https://sageox-design.netlify.app/catalog/cli/#c-prompt).

## API

Source: [`internal/cli/prompt.go`](../../../internal/cli/prompt.go)

```go
cli.Prompt(label string, defaultValue string) (string, error)
```

## Accessibility & fallbacks

- Default value rendered in dim style; pressing enter accepts it.
- TTY and non-TTY are the same interface.

## Tests

[`internal/cli/prompt_test.go`](../../../internal/cli/prompt_test.go)
