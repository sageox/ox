# Project Agent Instructions

This is a test project for ox CLI integration testing.

## Available Commands

- `ox agent prime` - Initialize agent session and receive team context
- `ox agent <id> guidance <path>` - Fetch progressive guidance for a specific path (infrastructure, conventions, etc.)
- `ox doctor` - Check project health
- `ox status` - Show authentication and project status

## Guidance System

ox uses a **progressive guidance** system: guidance is fetched on-demand for specific paths rather than loading everything upfront. Use `ox agent <id> guidance <path>` to fetch guidance relevant to a topic.

## Attribution

All commits must include:
```
Co-Authored-By: SageOx <ox@sageox.ai>
```
