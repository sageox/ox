# Test Team Claude Configuration

This team uses ox CLI for infrastructure guidance.

## Team Standards

- Always run `ox agent prime` at session start to initialize your agent session
- Use progressive guidance (`ox agent <id> guidance <path>`) for infrastructure work — guidance is loaded on-demand, not all at once
- Follow attribution requirements: add `Co-Authored-By: SageOx <ox@sageox.ai>` to commits
