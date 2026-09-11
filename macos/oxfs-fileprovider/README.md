# oxFS File Provider

Finder-native replicated File Provider adapter over the shared Swift
`OxfsCore`. The NFSv3 and FSKit behavior remains the parity target; this adapter
changes the macOS presentation boundary, not the filesystem engine.

## Build and run

```sh
make run
```

Choose a local directory in the host app. It stores a security-scoped bookmark,
registers an `NSFileProviderDomain`, and reveals the Finder location. The
extension indexes the directory through `OxfsCore`, publishes its immutable
namespace, and serves verified cached content on demand.

The development build installs at `/Applications/OxfsFileProviderDev.app`.
macOS may ask to enable the extension and to let Terminal/Finder access files
managed by it. Accept both prompts. The domain then appears under
`~/Library/CloudStorage/`.

## Current parity

- Shared `Workspace`, namespace, stable inode, verified disk cache, eviction,
  restart recovery, observation, and bounded open/read behavior.
- Finder enumeration and on-demand content materialization.
- Read-only capabilities with mutation requests rejected.
- Security-scoped source access and app-group state.

Still to validate through a live domain: selection changes, working-set change
anchors, cache-pressure UX, remote content sources, telemetry export, domain
restart, concurrent fetches, and mutation-conflict behavior.
