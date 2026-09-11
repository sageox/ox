import CryptoKit
import Foundation
import OxfsCore

private final class SnapshotDirectorySource: ContentSource, @unchecked Sendable {
    private let root: URL

    init(root: URL) {
        self.root = root
    }

    func fetch(_ reference: ContentRef, into output: ContentWriter) throws {
        let relative = reference.tenant.removingPercentEncoding ?? reference.tenant
        let handle = try FileHandle(forReadingFrom: root.appendingPathComponent(relative))
        defer { try? handle.close() }
        while let chunk = try handle.read(upToCount: 64 * 1024), !chunk.isEmpty {
            try output.write(Array(chunk))
        }
    }
}

enum LocalSnapshotPublisher {
    struct Delta {
        let changedFileInodes: [UInt64]
    }

    static func publish(
        source: URL,
        container: URL,
        cacheConfig: CacheConfig
    ) throws -> Delta {
        let identity = SHA256.hash(data: Data(source.path.utf8))
            .map { String(format: "%02x", $0) }
            .joined()
        let stateRoot = container
            .appendingPathComponent("workspaces", isDirectory: true)
            .appendingPathComponent(identity, isDirectory: true)
        let workspace = try Workspace.open(
            root: stateRoot,
            source: SnapshotDirectorySource(root: source),
            config: cacheConfig
        )
        let before = workspace.snapshot()
        let entries = try index(root: source)
        let current = workspace.manifest(sessionID: "fileprovider")?.generation ?? 0
        let outcome = try workspace.apply(
            Manifest(
                sessionID: "fileprovider",
                generation: current + 1,
                entries: entries
            )
        )
        guard outcome.applied else {
            throw CocoaError(.fileWriteUnknown)
        }
        let after = workspace.snapshot()
        let changed = after.nodes.values.compactMap { node -> UInt64? in
            guard node.kind == .file, node.file?.synthetic == nil else { return nil }
            guard let old = before.get(node.inode) else { return nil }
            return old.file?.content == node.file?.content ? nil : node.inode
        }
        return Delta(changedFileInodes: changed)
    }

    private static func index(root: URL) throws -> [ManifestEntry] {
        let keys: Set<URLResourceKey> = [
            .isRegularFileKey, .isSymbolicLinkKey,
            .contentModificationDateKey, .fileSizeKey
        ]
        guard let enumerator = FileManager.default.enumerator(
            at: root,
            includingPropertiesForKeys: Array(keys),
            options: [.skipsHiddenFiles, .skipsPackageDescendants]
        ) else {
            throw CocoaError(.fileReadUnknown)
        }

        var entries: [ManifestEntry] = []
        while let url = enumerator.nextObject() as? URL {
            let values = try url.resourceValues(forKeys: keys)
            if values.isSymbolicLink == true {
                enumerator.skipDescendants()
                continue
            }
            guard values.isRegularFile == true else { continue }
            let relative = String(url.path.dropFirst(root.path.count + 1))
            entries.append(
                try ManifestEntry(
                    path: relative,
                    sourceID: relative,
                    sourceKind: "FileProviderLocalSnapshot",
                    mode: 0o444,
                    mtimeSecs: UInt64(
                        values.contentModificationDate?.timeIntervalSince1970 ?? 0
                    ),
                    content: try contentReference(url, relative: relative),
                    reason: "published through macOS File Provider"
                )
            )
        }
        return entries.sorted { $0.path.asString < $1.path.asString }
    }

    private static func contentReference(_ url: URL, relative: String) throws -> ContentRef {
        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }
        var hasher = SHA256()
        var size: UInt64 = 0
        while let chunk = try handle.read(upToCount: 64 * 1024), !chunk.isEmpty {
            hasher.update(data: chunk)
            size += UInt64(chunk.count)
        }
        return try ContentRef(
            tenant: relative.addingPercentEncoding(withAllowedCharacters: .alphanumerics)
                ?? relative,
            algorithm: "sha256",
            digest: hasher.finalize().map { String(format: "%02x", $0) }.joined(),
            size: size
        )
    }
}
