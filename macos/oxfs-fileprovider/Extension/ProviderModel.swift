import CryptoKit
import FileProvider
import Foundation
import OxfsCore

let oxfsAppGroup = "C2LRSF5CK3.group.ai.sageox.oxfs.fileprovider"

private func providerStage<T>(_ name: String, _ body: () throws -> T) throws -> T {
    do {
        return try body()
    } catch {
        throw NSError(
            domain: "ai.sageox.oxfs.fileprovider",
            code: 1,
            userInfo: [
                NSLocalizedDescriptionKey: "File Provider initialization failed at \(name)",
                NSUnderlyingErrorKey: error
            ]
        )
    }
}

struct ProviderConfiguration: Codable {
    let sourceBookmark: Data
    let sourcePath: String
    let sourceName: String
    let deliveryMode: String?
    let workspaceKey: String?
    let cacheMaxBytes: UInt64?
    let cachePolicy: String?
}

struct ProviderTelemetry: Codable {
    let generation: UInt64
    let visibleFiles: Int
    let residentObjects: UInt64
    let residentBytes: UInt64
    let fetchedObjects: UInt64
    let fetchedBytes: UInt64
    let evictedObjects: UInt64
    let blockedEvictions: UInt64
    let enumerations: UInt64
    let contentFetches: UInt64
    let contentReadBytes: UInt64
    let updatedAt: Date
}

final class LocalDirectorySource: ContentSource, @unchecked Sendable {
    private let root: URL

    init(root: URL) {
        self.root = root
    }

    func fetch(_ reference: ContentRef, into output: ContentWriter) throws {
        let url = URL(fileURLWithPath: referenceSourceID(reference), relativeTo: root)
        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }
        while let chunk = try handle.read(upToCount: 64 * 1024), !chunk.isEmpty {
            try output.write(Array(chunk))
        }
    }

    // The local adapter stores the relative source path in the content tenant.
    private func referenceSourceID(_ reference: ContentRef) -> String {
        reference.tenant.removingPercentEncoding ?? reference.tenant
    }
}

final class ProviderModel: @unchecked Sendable {
    private var workspace: Workspace
    let sourceURL: URL
    private let containerURL: URL
    private let stateRoot: URL
    private let securityScopeActive: Bool
    private let staged: Bool
    private let cacheConfig: CacheConfig
    private let workspaceLock = NSLock()
    private let refreshLock = NSLock()
    private var sourceFingerprint = ""
    private var lastRemovedIdentifiers: [NSFileProviderItemIdentifier] = []
    private let metricsLock = NSLock()
    private var enumerations: UInt64 = 0
    private var contentFetches: UInt64 = 0
    private var contentReadBytes: UInt64 = 0

    init() throws {
        guard let container = FileManager.default.containerURL(
            forSecurityApplicationGroupIdentifier: oxfsAppGroup
        ) else {
            throw CocoaError(.fileNoSuchFile)
        }
        containerURL = container
        let data = try providerStage("read-configuration") {
            try Data(contentsOf: container.appendingPathComponent("configuration.json"))
        }
        let config = try providerStage("decode-configuration") {
            try JSONDecoder().decode(ProviderConfiguration.self, from: data)
        }
        let configuredURL = URL(fileURLWithPath: config.sourcePath)
        let configuredCache = try Self.cacheConfiguration(config)
        cacheConfig = configuredCache
        staged = config.deliveryMode == "staged"
        if staged || configuredURL.path.hasPrefix(container.path + "/") {
            sourceURL = configuredURL
            securityScopeActive = false
        } else {
            var stale = false
            sourceURL = try providerStage("resolve-source-bookmark") {
                try URL(
                    resolvingBookmarkData: config.sourceBookmark,
                    options: [.withSecurityScope],
                    relativeTo: nil,
                    bookmarkDataIsStale: &stale
                )
            }
            securityScopeActive = sourceURL.startAccessingSecurityScopedResource()
        }

        let source = LocalDirectorySource(root: sourceURL)
        let sourceIdentity = SHA256.hash(
            data: Data((config.workspaceKey ?? sourceURL.path).utf8)
        )
            .map { String(format: "%02x", $0) }
            .joined()
        let workspaceRoot = container
            .appendingPathComponent("workspaces", isDirectory: true)
            .appendingPathComponent(sourceIdentity, isDirectory: true)
        stateRoot = workspaceRoot
        workspace = try providerStage("open-workspace") {
            try Workspace.open(root: workspaceRoot, source: source, config: configuredCache)
        }

        _ = try providerStage("initial-refresh") { try refresh() }
        writeTelemetry()
    }

    deinit {
        if securityScopeActive {
            sourceURL.stopAccessingSecurityScopedResource()
        }
    }

    func node(for identifier: NSFileProviderItemIdentifier) -> Node? {
        let snapshot = workspaceSnapshot()
        if identifier == .rootContainer {
            return snapshot.get(ROOT_INODE)
        }
        guard identifier.rawValue.hasPrefix("inode:"),
              let inode = UInt64(identifier.rawValue.dropFirst("inode:".count)) else {
            return nil
        }
        return snapshot.get(inode)
    }

    func item(for node: Node) -> ProviderItem {
        ProviderItem(node: node, generation: workspaceSnapshot().generation)
    }

    func workspaceSnapshot() -> Namespace {
        workspaceLock.lock()
        defer { workspaceLock.unlock() }
        return workspace.snapshot()
    }

    func openInode(_ inode: UInt64, expectedContent: ContentRef?) throws -> OpenFile {
        workspaceLock.lock()
        defer { workspaceLock.unlock() }
        return try workspace.openInode(inode, expectedContent: expectedContent)
    }

    @discardableResult
    func refresh() throws -> Bool {
        refreshLock.lock()
        defer { refreshLock.unlock() }
        if staged {
            let reloaded = try Workspace.open(
                root: stateRoot,
                source: LocalDirectorySource(root: sourceURL),
                config: cacheConfig
            )
            let oldSnapshot = workspaceSnapshot()
            let newSnapshot = reloaded.snapshot()
            guard newSnapshot.generation != oldSnapshot.generation else {
                return false
            }
            let oldIdentifiers = Set(
                oldSnapshot.nodes.keys.filter { $0 != ROOT_INODE }
                    .map { NSFileProviderItemIdentifier("inode:\($0)") }
            )
            let newIdentifiers = Set(
                newSnapshot.nodes.keys.filter { $0 != ROOT_INODE }
                    .map { NSFileProviderItemIdentifier("inode:\($0)") }
            )
            workspaceLock.lock()
            workspace = reloaded
            workspaceLock.unlock()
            lastRemovedIdentifiers = Array(oldIdentifiers.subtracting(newIdentifiers))
            sourceFingerprint = "staged:\(newSnapshot.generation)"
            writeTelemetry()
            return true
        }

        let indexed = try Self.index(root: sourceURL)
        let fingerprint = Self.fingerprint(indexed)
        if fingerprint == sourceFingerprint {
            return false
        }

        let oldIdentifiers = Set(
            workspace.snapshot().nodes.keys
                .filter { $0 != ROOT_INODE }
                .map { NSFileProviderItemIdentifier("inode:\($0)") }
        )
        let current = workspace.manifest(sessionID: "fileprovider")?.generation ?? 0
        let outcome = try workspace.apply(
            Manifest(sessionID: "fileprovider", generation: current + 1, entries: indexed)
        )
        guard outcome.applied else { return false }

        let newIdentifiers = Set(
            workspace.snapshot().nodes.keys
                .filter { $0 != ROOT_INODE }
                .map { NSFileProviderItemIdentifier("inode:\($0)") }
        )
        lastRemovedIdentifiers = Array(oldIdentifiers.subtracting(newIdentifiers))
        sourceFingerprint = fingerprint
        return true
    }

    func removedIdentifiers() -> [NSFileProviderItemIdentifier] {
        refreshLock.lock()
        defer { refreshLock.unlock() }
        return lastRemovedIdentifiers
    }

    func syncAnchor() -> NSFileProviderSyncAnchor {
        NSFileProviderSyncAnchor(Data(String(workspaceSnapshot().generation).utf8))
    }

    func recordEnumeration() {
        metricsLock.lock()
        enumerations &+= 1
        metricsLock.unlock()
        writeTelemetry()
    }

    func recordContentFetch(bytes: UInt64) {
        metricsLock.lock()
        contentFetches &+= 1
        contentReadBytes &+= bytes
        metricsLock.unlock()
        writeTelemetry()
    }

    private func writeTelemetry() {
        workspaceLock.lock()
        defer { workspaceLock.unlock() }
        guard let cache = try? workspace.cacheTelemetry() else { return }
        let (evicted, blocked) = workspace.evictionCounters()
        let snapshotGeneration = workspace.snapshot()
        let visible = snapshotGeneration.nodes.values.filter {
            $0.kind == .file && ($0.file?.synthetic == nil)
        }.count
        metricsLock.lock()
        let snapshot = ProviderTelemetry(
            generation: snapshotGeneration.generation,
            visibleFiles: visible,
            residentObjects: cache.residentObjects,
            residentBytes: cache.residentBytes,
            fetchedObjects: cache.fetchedObjects,
            fetchedBytes: cache.fetchedBytes,
            evictedObjects: evicted,
            blockedEvictions: blocked,
            enumerations: enumerations,
            contentFetches: contentFetches,
            contentReadBytes: contentReadBytes,
            updatedAt: Date()
        )
        metricsLock.unlock()
        try? JSONEncoder().encode(snapshot).write(
            to: containerURL.appendingPathComponent("telemetry.json"),
            options: .atomic
        )
    }

    private static func fingerprint(_ entries: [ManifestEntry]) -> String {
        let value = entries.map {
            "\($0.path.asString)\u{0}\($0.content.digest)\u{0}\($0.content.size)\u{0}\($0.mtimeSecs)"
        }.joined(separator: "\u{1e}")
        return SHA256.hash(data: Data(value.utf8))
            .map { String(format: "%02x", $0) }
            .joined()
    }

    private static func cacheConfiguration(_ config: ProviderConfiguration) throws -> CacheConfig {
        let maxBytes = config.cacheMaxBytes ?? DEFAULT_CACHE_MAX_BYTES
        guard maxBytes > 0 else { throw CocoaError(.fileReadCorruptFile) }
        let order: EvictionOrder
        switch config.cachePolicy ?? "lrs" {
        case "lrs": order = .leastRecentlySelectedGeneration
        case "clock": order = .clockSecondChance
        case "lfu": order = .approxLeastFrequentlyUsed
        default: throw CocoaError(.fileReadCorruptFile)
        }
        return CacheConfig(maxBytes: maxBytes, eviction: order)
    }

    private static func index(root: URL) throws -> [ManifestEntry] {
        let keys: [URLResourceKey] = [
            .isRegularFileKey, .isDirectoryKey, .isSymbolicLinkKey,
            .contentModificationDateKey, .fileSizeKey
        ]
        guard let enumerator = FileManager.default.enumerator(
            at: root,
            includingPropertiesForKeys: keys,
            options: [.skipsHiddenFiles, .skipsPackageDescendants]
        ) else {
            throw CocoaError(.fileReadUnknown)
        }

        var entries: [ManifestEntry] = []
        while let url = enumerator.nextObject() as? URL {
            let values = try url.resourceValues(forKeys: Set(keys))
            if values.isSymbolicLink == true {
                enumerator.skipDescendants()
                continue
            }
            guard values.isRegularFile == true else { continue }
            let relative = String(url.path.dropFirst(root.path.count + 1))
            let content = try contentReference(for: url, relativePath: relative)
            entries.append(
                try ManifestEntry(
                    path: relative,
                    sourceID: relative,
                    sourceKind: "FileProviderLocalDirectory",
                    mode: 0o444,
                    mtimeSecs: UInt64(values.contentModificationDate?.timeIntervalSince1970 ?? 0),
                    content: content,
                    reason: "published through macOS File Provider"
                )
            )
        }
        return entries.sorted { $0.path.asString < $1.path.asString }
    }

    private static func contentReference(for url: URL, relativePath: String) throws -> ContentRef {
        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }
        var hasher = SHA256()
        var size: UInt64 = 0
        while let chunk = try handle.read(upToCount: 64 * 1024), !chunk.isEmpty {
            hasher.update(data: chunk)
            size += UInt64(chunk.count)
        }
        let digest = hasher.finalize().map { String(format: "%02x", $0) }.joined()
        return try ContentRef(
            tenant: relativePath.addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? relativePath,
            algorithm: "sha256",
            digest: digest,
            size: size
        )
    }
}
