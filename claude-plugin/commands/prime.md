Load SageOx team context for this AI coworker session.

Use when:
- Starting a new coding session in a repo with shared team context
- After context compaction or clear operations
- When you need team conventions, norms, or architectural decisions
- Before making changes to understand team patterns

Keywords: prime, session start, guidance, team context, conventions, init session

## Common Issues

### ox not found
**Symptom:** `command not found: ox`
**Solution:** Install ox CLI: `brew install sageox/tap/ox` or see https://sageox.ai/install

### No guidance loaded
**Symptom:** Prime runs but returns empty guidance
**Solution:** Run `ox init` first to initialize SageOx in this repository

### Stale guidance
**Symptom:** Guidance doesn't reflect recent changes
**Solution:** Run `ox agent prime` again to reload from source

$ox agent prime
