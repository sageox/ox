import Foundation

/// Port of `crates/oxfs/src/workspace.rs::Workspace`.
///
/// Owns the namespace generations, the stable inode table, the verified cache,
/// the observation log, and the per-session desired manifests. `apply` fetches
/// and verifies the working set *before* the replacement namespace becomes
/// visible (eager materialize-then-publish — oxFS is deliberately not demand
/// paged). Metadata ops (`lookup`/`getattr`/`readdir`) never touch content.
public final class Workspace {
    private let cache: ContentCache
    private let inodes: InodeTable
    private let observations: ObservationLog
    private let selections: SelectionStore

    private let stateLock = NSLock()     // guards `namespace` + `sessions`
    private let reconcileLock = NSLock() // serializes `apply`
    private var namespace: Namespace
    private var sessions: [String: Manifest]

    private init(cache: ContentCache, inodes: InodeTable, observations: ObservationLog,
                 selections: SelectionStore, sessions: [String: Manifest]) {
        self.cache = cache
        self.inodes = inodes
        self.observations = observations
        self.selections = selections
        self.sessions = sessions
        self.namespace = Namespace.empty()
    }

    public static func open(root: URL, source: ContentSource,
                            config: CacheConfig = .default) throws -> Workspace {
        let cache = try DiskContentCache(root: root.appendingPathComponent("cache"), source: source, config: config)
        return try open(root: root, cache: cache)
    }

    /// Open with an explicit cache (used by tests and, later, the disk cache).
    public static func open(root: URL, cache: ContentCache) throws -> Workspace {
        let state = root.appendingPathComponent("state")
        let observations = try ObservationLog(stateDir: state)
        let selections = try SelectionStore(stateDir: state)
        let sessions = selections.load() ?? [:]
        let inodes = try InodeTable(stateDir: state)
        let ws = Workspace(cache: cache, inodes: inodes, observations: observations,
                           selections: selections, sessions: sessions)

        // Restore as much of the persisted desired set as the cap permits.
        var available = Set<String>()
        for entry in ws.orderedEntries(sessions) where try cache.resident(entry.content) != nil {
            available.insert(cache.storageKey(entry.content))
        }
        try cache.beginBatch(ws.orderedEntries(sessions).map { $0.content })
        var stopped = false
        for entry in ws.orderedEntries(sessions) {
            let key = cache.storageKey(entry.content)
            if available.contains(key) { continue }
            if stopped || entry.content.size > cache.capacity() { stopped = true; continue }
            do {
                _ = try cache.materializeMissing(entry.content)
                available.insert(key)
            } catch {
                stopped = true
            }
        }
        try cache.commitBatch()
        ws.namespace = try ws.buildNamespace(sessions, available)
        // If a prior process stopped between cache reconciliation and selection
        // publication, loading preferred the pending intent. The successful
        // restore above completes that transaction.
        try selections.commitStaged()
        return ws
    }

    public func snapshot() -> Namespace {
        stateLock.lock(); defer { stateLock.unlock() }
        return namespace
    }

    public func manifest(sessionID: String) -> Manifest? {
        stateLock.lock(); defer { stateLock.unlock() }
        return sessions[sessionID]
    }

    /// Apply a complete per-session desired set. Faithful port of `apply`.
    public func apply(_ manifest: Manifest) throws -> ApplyOutcome {
        reconcileLock.lock(); defer { reconcileLock.unlock() }
        try manifest.validate()

        // Stale-generation guard.
        if let current = currentSessions()[manifest.sessionID],
           manifest.generation <= current.generation {
            return ApplyOutcome(applied: false, available: 0, stopped: 0)
        }

        var candidate = currentSessions()
        let admissions = manifest.entries
        candidate[manifest.sessionID] = manifest

        // Pin the candidate's resident objects and the still-published
        // namespace's objects: the old namespace stays readable until the swap.
        var protected: [String: UInt64] = [:]
        var available = Set<String>()
        for entry in orderedEntries(candidate) where try cache.resident(entry.content) != nil {
            protected[cache.storageKey(entry.content)] = entry.content.size
            available.insert(cache.storageKey(entry.content))
        }
        let published = snapshot()
        for node in published.nodes.values {
            guard let file = node.file, file.synthetic == nil else { continue }
            let key = cache.storageKey(file.content)
            if protected[key] == nil, try cache.resident(file.content) != nil {
                protected[key] = file.content.size
            }
        }

        // Refuse a transient replacement squeeze rather than publish a subset.
        var candidateBytes: [String: UInt64] = [:]
        for entry in orderedEntries(candidate) {
            candidateBytes[cache.storageKey(entry.content)] = entry.content.size
        }
        let needed = candidateBytes.values.reduce(0, +)
        let pinned = protected.filter { candidateBytes[$0.key] == nil }.values.reduce(0, +)
        let capacity = cache.capacity()
        if needed <= capacity && needed + pinned > capacity {
            throw WorkspaceError.replacementCapacity(needed: needed, pinned: pinned, capacity: capacity)
        }

        let candidates = admissions.map {
            (key: cache.storageKey($0.content), content: $0.content, size: $0.content.size)
        }
        let missing = AdmitEverything.select(capacity: capacity, resident: available, candidates: candidates)

        try selections.stage(candidate)
        do {
            try cache.beginBatch(admissions.map { $0.content })
        } catch {
            try? selections.rollbackStaged()
            throw error
        }
        do {
            let materialized = try cache.materializeMissingBatch(missing)
            available.formUnion(materialized)
            _ = try buildNamespace(candidate, available) // validate before commit
            try cache.commitBatch()
        } catch {
            try? cache.rollbackBatch()
            try? selections.rollbackStaged()
            throw Self.mapError(error)
        }

        // Recompute residency post-commit and build the generation to publish.
        available = try residentDesiredKeys(candidate)
        let next = try buildNamespace(candidate, available)
        let availableCount = next.nodes.values.filter {
            $0.kind == .file && ($0.file.map { $0.synthetic == nil } ?? false)
        }.count
        let desiredPaths = Set(orderedEntries(candidate).map { $0.path.asString })
        let stoppedCount = desiredPaths.count - availableCount

        try selections.commitStaged()
        stateLock.lock()
        sessions = candidate
        namespace = next
        stateLock.unlock()

        return ApplyOutcome(applied: true, available: availableCount, stopped: stoppedCount)
    }

    public func openInode(_ inode: UInt64, expectedContent: ContentRef? = nil) throws -> OpenFile {
        // Revalidate and bind while publication is excluded. Once
        // `cache.openContent` returns, its descriptor pin—not this lock—keeps
        // the exact object alive across namespace replacement.
        stateLock.lock(); defer { stateLock.unlock() }
        guard let node = namespace.get(inode) else { throw WorkspaceError.notFound }
        guard node.kind == .file, let file = node.file else { throw WorkspaceError.isDirectory }
        if let expectedContent, file.content != expectedContent { throw WorkspaceError.stale }
        let backing: OpenContent
        if let synthetic = file.synthetic {
            backing = SyntheticOpenContent(bytes: synthetic)
        } else {
            backing = try cache.openContent(file.content)
        }
        try observations.append(.open, inode: inode, path: node.path, offset: 0, bytes: 0)
        return OpenFile(node: node, backing: backing, observations: observations)
    }

    public func dismiss(_ inode: UInt64) throws {
        guard let node = snapshot().get(inode) else { throw WorkspaceError.notFound }
        try observations.append(.dismiss, inode: inode, path: node.path, offset: 0, bytes: 0)
    }

    // MARK: - telemetry / introspection

    public func cacheTelemetry() throws -> CacheTelemetrySnapshot { try cache.telemetry() }
    public func evictionCounters() -> (evicted: UInt64, blocked: UInt64) { cache.evictionCounters() }
    public func cacheCapacity() -> (capacity: UInt64, remaining: UInt64) {
        (cache.capacity(), cache.remaining())
    }
    public func residentKeys() throws -> [String] { try cache.residentKeys() }

    // MARK: - namespace construction

    private func buildNamespace(_ sessions: [String: Manifest], _ available: Set<String>) throws -> Namespace {
        // Group every selecting entry by canonical path, in session-id order so
        // ties break deterministically (lowest session id wins entries[0]).
        var grouped: [String: [(sessionID: String, entry: ManifestEntry)]] = [:]
        var maxGeneration: UInt64 = 0
        for sessionID in sessions.keys.sorted() {
            let manifest = sessions[sessionID]!
            maxGeneration = Swift.max(maxGeneration, manifest.generation)
            for entry in manifest.entries {
                grouped[entry.path.asString, default: []].append((sessionID, entry))
            }
        }

        var desired: [String: DesiredIndex] = [:]
        let published = snapshot()
        for (path, entries) in grouped {
            let publishedContent = published.byPath(path)?.file?.content
            let winner = entries.first(where: {
                $0.entry.content == publishedContent
                    && available.contains(cache.storageKey($0.entry.content))
            })?.entry
                ?? entries.first(where: { available.contains(cache.storageKey($0.entry.content)) })?.entry
                ?? entries[0].entry
            let collided = entries.contains { $0.entry.content != winner.content }
            let winnerResident = available.contains(cache.storageKey(winner.content))
            let pathStatus: Status = winnerResident ? .available
                : collided ? .pathCollision
                : winner.content.size > cache.capacity() ? .exceedsCacheLimit
                : .noSpace
            let selectors = entries.map { pair in
                Selector(
                    sessionID: pair.sessionID,
                    reason: pair.entry.reason,
                    status: pair.entry.content == winner.content ? pathStatus : .pathCollision
                )
            }
            desired[path] = DesiredIndex(entry: winner, selectors: selectors, status: pathStatus)
        }

        let root = Node.root()
        var nodes: [UInt64: Node] = [ROOT_INODE: root]
        var byPath: [String: UInt64] = ["": ROOT_INODE]

        for path in desired.keys.sorted() {
            let item = desired[path]!
            guard item.status == .available else { continue } // WYSIWYG: only resident winners are visible
            let entry = item.entry
            let parts = path.split(separator: "/", omittingEmptySubsequences: false).map(String.init)
            var parent = ROOT_INODE
            var prefix = ""
            for (index, part) in parts.enumerated() {
                if !prefix.isEmpty { prefix += "/" }
                prefix += part
                let inode = try inodes.inode(for: prefix)
                if nodes[inode] == nil {
                    let isFile = index + 1 == parts.count
                    let node: Node
                    if isFile {
                        node = Node(
                            inode: inode, name: part, path: prefix, parent: parent,
                            kind: .file, mode: entry.mode, size: entry.content.size,
                            mtimeSecs: entry.mtimeSecs,
                            file: FileNode(
                                content: entry.content, sourceID: entry.sourceID,
                                sourceKind: entry.sourceKind, reason: entry.reason,
                                selectors: item.selectors, synthetic: nil
                            )
                        )
                    } else {
                        node = Node(
                            inode: inode, name: part, path: prefix, parent: parent,
                            kind: .directory, mode: 0o555, size: 0,
                            mtimeSecs: entry.mtimeSecs, file: nil
                        )
                    }
                    nodes[inode] = node
                    byPath[prefix] = inode
                    nodes[parent]!.children[part] = inode
                }
                parent = inode
            }
        }

        try addIndexes(&nodes, &byPath, generation: maxGeneration, desired: desired)
        try inodes.sync() // one durability boundary before publish

        return Namespace(generation: maxGeneration, nodes: nodes, byPathIndex: byPath)
    }

    /// Build the always-resident synthetic `.sageox/INDEX.{md,json}`.
    /// Port of `workspace.rs::add_indexes`.
    private func addIndexes(_ nodes: inout [UInt64: Node], _ byPath: inout [String: UInt64],
                            generation: UInt64, desired: [String: DesiredIndex]) throws {
        let dirInode = try inodes.inode(for: ".sageox")
        let mdInode = try inodes.inode(for: ".sageox/INDEX.md")
        let jsonInode = try inodes.inode(for: ".sageox/INDEX.json")

        var markdown = "# oxFS working set\n\nGeneration: `\(generation)`\n\n| Path | Size | Kind | Status | Selected by | Why |\n|---|---:|---|---|---|---|\n"
        var json = "{\"generation\":\(generation),\"files\":["

        for (index, path) in desired.keys.sorted().enumerated() {
            let item = desired[path]!
            let file = item.entry
            let status = item.status.rawValue
            let selectors = item.selectors.map { $0.sessionID }.joined(separator: ", ")
            let reasons = item.selectors.map { $0.reason }.joined(separator: "; ")
            markdown += "| `\(markdownEscape(path))` | \(file.content.size) | \(markdownEscape(file.sourceKind)) | \(status) | \(markdownEscape(selectors)) | \(markdownEscape(reasons)) |\n"

            if index != 0 { json += "," }
            json += "{\"path\":\(jsonString(path)),\"size\":\(file.content.size),\"source_id\":\(jsonString(file.sourceID)),\"source_kind\":\(jsonString(file.sourceKind)),\"status\":\"\(status)\",\"selectors\":["
            for (si, selector) in item.selectors.enumerated() {
                if si != 0 { json += "," }
                json += "{\"session_id\":\(jsonString(selector.sessionID)),\"reason\":\(jsonString(selector.reason)),\"status\":\"\(selector.status.rawValue)\"}"
            }
            json += "]}"
        }
        json += "]}\n"

        let directory = Node(
            inode: dirInode, name: ".sageox", path: ".sageox", parent: ROOT_INODE,
            kind: .directory, mode: 0o555, size: 0, mtimeSecs: 0, file: nil,
            children: ["INDEX.json": jsonInode, "INDEX.md": mdInode]
        )
        nodes[ROOT_INODE]!.children[".sageox"] = dirInode
        nodes[dirInode] = directory
        byPath[".sageox"] = dirInode

        let syntheticRef = try ContentRef(tenant: "synthetic", algorithm: "sha256", digest: "00", size: 0)
        for (inode, name, bytes) in [
            (mdInode, "INDEX.md", Array(markdown.utf8)),
            (jsonInode, "INDEX.json", Array(json.utf8)),
        ] {
            let path = ".sageox/\(name)"
            nodes[inode] = Node(
                inode: inode, name: name, path: path, parent: dirInode,
                kind: .file, mode: 0o444, size: UInt64(bytes.count), mtimeSecs: 0,
                file: FileNode(
                    content: syntheticRef, sourceID: "oxfs", sourceKind: "Index",
                    reason: "mount-global working-set index", selectors: [], synthetic: bytes
                )
            )
            byPath[path] = inode
        }
    }

    // MARK: - helpers

    private func currentSessions() -> [String: Manifest] {
        stateLock.lock(); defer { stateLock.unlock() }
        return sessions
    }

    /// All entries across sessions, in session-id then declaration order.
    private func orderedEntries(_ sessions: [String: Manifest]) -> [ManifestEntry] {
        sessions.keys.sorted().flatMap { sessions[$0]!.entries }
    }

    private func residentDesiredKeys(_ sessions: [String: Manifest]) throws -> Set<String> {
        var resident = Set<String>()
        for entry in orderedEntries(sessions) where try cache.resident(entry.content) != nil {
            resident.insert(cache.storageKey(entry.content))
        }
        return resident
    }

    private static func mapError(_ error: Error) -> Error {
        error is WorkspaceError ? error : error
    }
}

/// A resolved winner for a canonical path plus its selectors and aggregate
/// status. Port of the Rust `DesiredIndex`.
private struct DesiredIndex {
    let entry: ManifestEntry
    let selectors: [Selector]
    let status: Status
}

public struct ApplyOutcome: Equatable, Sendable {
    public let applied: Bool
    public let available: Int
    public let stopped: Int
}

/// An open handle to a resident (or synthetic) file. Reads slice the backing
/// bytes and append a `read` observation, mirroring `workspace.rs::OpenFile`.
public final class OpenFile {
    private let node: Node
    private let backing: OpenContent
    private let observations: ObservationLog

    init(node: Node, backing: OpenContent, observations: ObservationLog) {
        self.node = node
        self.backing = backing
        self.observations = observations
    }

    public var inode: UInt64 { node.inode }
    public var size: UInt64 { node.size }

    public func read(offset: UInt64, count: Int) throws -> [UInt8] {
        let data = try backing.read(offset: offset, count: count)
        try observations.append(.read, inode: node.inode, path: node.path, offset: offset, bytes: data.count)
        return data
    }

    public func close() throws { try backing.close() }
    deinit { try? backing.close() }
}

private final class SyntheticOpenContent: OpenContent {
    private let bytes: [UInt8]
    init(bytes: [UInt8]) { self.bytes = bytes }
    var size: UInt64 { UInt64(bytes.count) }
    func read(offset: UInt64, count: Int) throws -> [UInt8] { readRange(bytes, offset: offset, count: count) }
    func close() throws {}
}

public enum WorkspaceError: Error, Equatable {
    case io(String)
    case notFound
    case isDirectory
    case stale
    case readOnly
    case replacementCapacity(needed: UInt64, pinned: UInt64, capacity: UInt64)
    case poisoned
}

private func markdownEscape(_ value: String) -> String {
    value.replacingOccurrences(of: "|", with: "\\|").replacingOccurrences(of: "\n", with: " ")
}

private func jsonString(_ value: String) -> String {
    ObservationLog.jsonString(value)
}
