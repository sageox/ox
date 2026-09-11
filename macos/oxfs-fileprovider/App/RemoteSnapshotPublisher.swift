import CryptoKit
import Foundation
import OxfsCore

private func remoteStage<T>(_ name: String, _ body: () throws -> T) throws -> T {
    do {
        return try body()
    } catch {
        throw NSError(
            domain: "ai.sageox.oxfs.fileprovider.remote",
            code: 1,
            userInfo: [
                NSLocalizedDescriptionKey: "Remote publication failed at \(name)",
                NSUnderlyingErrorKey: error
            ]
        )
    }
}

private func remoteFailure(_ name: String, _ error: Error) -> Error {
    NSError(
        domain: "ai.sageox.oxfs.fileprovider.remote",
        code: 1,
        userInfo: [
            NSLocalizedDescriptionKey: "Remote publication failed at \(name)",
            NSUnderlyingErrorKey: error
        ]
    )
}

enum RemoteSnapshotPublisher {
    static func publish(
        host: String,
        selection: String,
        workspaceKey: String,
        container: URL,
        cacheConfig: CacheConfig
    ) throws {
        let registry = try remoteStage("discover-local-repositories") {
            try RemoteRegistry.discover(host: host)
        }
        let credentials = try remoteStage("read-ox-credentials") {
            try OxCredentialProvider().credentials(host: host)
        }
        let remote = try remoteStage("create-remote-source") {
            try RemoteContentSource(
                host: host,
                credentials: credentials,
                transport: URLSessionRemoteTransport()
            )
        }
        let identity = SHA256.hash(data: Data(workspaceKey.utf8))
            .map { String(format: "%02x", $0) }
            .joined()
        let stateRoot = container
            .appendingPathComponent("workspaces", isDirectory: true)
            .appendingPathComponent(identity, isDirectory: true)
        let workspace = try remoteStage("open-workspace") {
            try Workspace.open(root: stateRoot, source: remote, config: cacheConfig)
        }
        let capacity = workspace.cacheCapacity().capacity
        let resolved: [RemoteResolvedFile]
        do {
            resolved = [
                try remote.resolveFile(
                    registry: registry,
                    selection: selection,
                    maxBytes: capacity
                )
            ]
        } catch RemoteError.directoryRequiresArchive {
            resolved = try remoteStage("resolve-directory") {
                try remote.resolveDirectory(
                    registry: registry,
                    selection: selection,
                    maxBytes: capacity
                )
            }
        } catch {
            throw remoteFailure("resolve-file", error)
        }
        let entries = try resolved.map {
            try ManifestEntry(
                path: $0.virtualPath,
                sourceID: $0.repoPath,
                sourceKind: "FileProviderRemote",
                mode: $0.mode,
                mtimeSecs: $0.mtimeSecs,
                content: $0.content,
                reason: "human-selected SageOx remote path"
            )
        }
        let current = workspace.manifest(sessionID: "fileprovider")?.generation ?? 0
        let outcome = try remoteStage("apply-manifest") {
            try workspace.apply(
                Manifest(
                    sessionID: "fileprovider",
                    generation: current + 1,
                    entries: entries
                )
            )
        }
        guard outcome.applied else { throw CocoaError(.fileWriteUnknown) }
    }
}
