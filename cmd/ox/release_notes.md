# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.1] - 2026-02-19

### Added
- Pre-built binaries for 6 platforms (curl one-liner install)
- Ed25519 artifact signing

### Changed
- Daemon liveness uses socket-ping instead of flock
- All API calls are endpoint-aware

### Fixed
- `ox sync` now surfaces daemon errors instead of silent success (#9)
- `ox status` crash on empty ledger repos
- `ox doctor --fix` discovers uncloned team contexts
- Git credentials masked in error output

## [0.1.0] - 2026-02-18

Initial public release of the SageOx CLI (`ox`).

### Highlights

- **Session recording**: Capture, view, and export human-AI coding sessions with HTML and Markdown output
- **Team discussion**: Record and transcribe team conversations so arch decisions and product context flows automatically to agents
- **Background daemon**: Automatic git sync for ledgers and team contexts with self-healing clone recovery

[0.1.1]: https://github.com/sageox/ox/releases/tag/v0.1.1
[0.1.0]: https://github.com/sageox/ox/releases/tag/v0.1.0
