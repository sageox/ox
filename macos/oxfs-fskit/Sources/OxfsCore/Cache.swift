import Foundation

public let DEFAULT_CACHE_MAX_BYTES: UInt64 = 1024 * 1024 * 1024

/// Cache eviction order. Port of `cache_policy::EvictionOrder`.
public enum EvictionOrder: Sendable, Equatable {
    /// Default: evict the least-recently-*selected* generation (by access epoch),
    /// deterministic within a generation by key. Reads do not affect recency.
    case leastRecentlySelectedGeneration
    /// CLOCK second-chance: a reference bit gives a touched object one reprieve.
    case clockSecondChance
    /// Sampled/approximate LFU: evict the least-frequently-opened object.
    case approxLeastFrequentlyUsed
}

public struct CacheConfig: Sendable {
    public var maxBytes: UInt64
    public var eviction: EvictionOrder
    public init(maxBytes: UInt64 = DEFAULT_CACHE_MAX_BYTES,
                eviction: EvictionOrder = .leastRecentlySelectedGeneration) {
        self.maxBytes = maxBytes
        self.eviction = eviction
    }
    public static let `default` = CacheConfig()
}

public struct ResidentContent: Sendable, Equatable {
    public let storageKey: String
    public let size: UInt64
}

public struct CacheTelemetrySnapshot: Sendable, Equatable {
    public var fetchedObjects: UInt64 = 0
    public var fetchedBytes: UInt64 = 0
    public var residentObjects: UInt64 = 0
    public var residentBytes: UInt64 = 0
    public var pendingObjects: UInt64 = 0
    public var directorySyncs: UInt64 = 0
    public var catalogTransactions: UInt64 = 0
}

/// The Workspace-facing cache surface. `MemoryContentCache` (Phase 2) and the
/// forthcoming disk-backed crash-safe cache (Phase 3) both conform, so the
/// Workspace and its conformance tests are cache-implementation agnostic.
public protocol ContentCache: AnyObject {
    func storageKey(_ reference: ContentRef) -> String
    func capacity() -> UInt64
    func remaining() -> UInt64
    func used() -> UInt64
    func resident(_ reference: ContentRef) throws -> ResidentContent?
    func residentKeys() throws -> [String]
    func evictionCounters() -> (evicted: UInt64, blocked: UInt64)
    func telemetry() throws -> CacheTelemetrySnapshot
    func beginBatch(_ admissions: [ContentRef]) throws
    func materialize(_ reference: ContentRef) throws -> ResidentContent
    func materializeMissing(_ reference: ContentRef) throws -> ResidentContent
    func materializeMissingBatch(_ references: [ContentRef]) throws -> [String]
    func commitBatch() throws
    func rollbackBatch() throws
    func openBytes(_ reference: ContentRef) throws -> [UInt8]
}

extension ContentCache {
    public func storageKey(_ reference: ContentRef) -> String { reference.storageKey }
}

/// Ranked admission: take candidates in order, skipping already-resident and
/// duplicate keys, stopping entirely at the first object larger than the whole
/// cache. Faithful port of `cache_policy::AdmitEverything::select`.
public enum AdmitEverything {
    public static func select(
        capacity: UInt64,
        resident: Set<String>,
        candidates: [(key: String, content: ContentRef, size: UInt64)]
    ) -> [ContentRef] {
        var seen = Set<String>()
        var selected: [ContentRef] = []
        for candidate in candidates {
            if resident.contains(candidate.key) || !seen.insert(candidate.key).inserted {
                continue
            }
            if candidate.size > capacity { break }
            selected.append(candidate.content)
        }
        return selected
    }
}

/// Slice `[offset, offset+count)` from resident bytes, clamped to EOF.
/// Port of `cache::read_range`.
public func readRange(_ bytes: [UInt8], offset: UInt64, count: Int) -> [UInt8] {
    let start = Int(Swift.min(offset, UInt64(Int.max)))
    if start >= bytes.count || count <= 0 { return [] }
    // Avoid `start + count` overflow when callers pass a huge count (e.g. Int.max).
    let n = Swift.min(count, bytes.count - start)
    return Array(bytes[start..<start + n])
}

/// In-memory content cache for Phase 2: fetch → verify size + SHA-256 → admit,
/// with a byte cap and LRU eviction of unpinned objects. The disk-backed
/// crash-safe cache (Phase 3) will replace this behind `ContentCache`.
public final class MemoryContentCache: ContentCache {
    private let config: CacheConfig
    private let source: ContentSource
    private let lock = NSLock()

    private var objects: [String: [UInt8]] = [:]
    private var lru: [String] = [] // least-recent first, most-recent last

    private var inBatch = false
    private var pinned: Set<String> = []
    private var batchAdded: [String] = []

    private var fetchedObjects: UInt64 = 0
    private var fetchedBytes: UInt64 = 0
    private var directorySyncs: UInt64 = 0
    private var evicted: UInt64 = 0
    private var blocked: UInt64 = 0

    public init(source: ContentSource, config: CacheConfig = .default) {
        self.source = source
        self.config = config
    }

    public func capacity() -> UInt64 { config.maxBytes }

    public func used() -> UInt64 {
        lock.lock(); defer { lock.unlock() }
        return usedLocked()
    }

    public func remaining() -> UInt64 {
        lock.lock(); defer { lock.unlock() }
        return config.maxBytes - Swift.min(config.maxBytes, usedLocked())
    }

    public func resident(_ reference: ContentRef) throws -> ResidentContent? {
        lock.lock(); defer { lock.unlock() }
        return residentLocked(reference)
    }

    public func residentKeys() throws -> [String] {
        lock.lock(); defer { lock.unlock() }
        return objects.keys.sorted()
    }

    public func evictionCounters() -> (evicted: UInt64, blocked: UInt64) {
        lock.lock(); defer { lock.unlock() }
        return (evicted, blocked)
    }

    public func telemetry() throws -> CacheTelemetrySnapshot {
        lock.lock(); defer { lock.unlock() }
        return CacheTelemetrySnapshot(
            fetchedObjects: fetchedObjects,
            fetchedBytes: fetchedBytes,
            residentObjects: UInt64(objects.count),
            residentBytes: usedLocked(),
            pendingObjects: inBatch ? UInt64(batchAdded.count) : 0,
            directorySyncs: directorySyncs
        )
    }

    public func beginBatch(_ admissions: [ContentRef]) throws {
        lock.lock(); defer { lock.unlock() }
        inBatch = true
        pinned = Set(admissions.map { $0.storageKey })
        // Objects already resident (the still-live selection) are pinned too, so
        // an in-flight admission never evicts the published working set.
        pinned.formUnion(objects.keys)
        batchAdded = []
    }

    public func materialize(_ reference: ContentRef) throws -> ResidentContent {
        if let rc = try resident(reference) { return rc }
        return try materializeMissing(reference)
    }

    public func materializeMissing(_ reference: ContentRef) throws -> ResidentContent {
        let key = reference.storageKey
        // Fast path: already resident.
        if let rc = try resident(reference) { return rc }
        if reference.size > config.maxBytes { throw FetchError.io("StorageFull") }

        // Fetch outside the lock so a slow origin doesn't serialize the cache.
        let sink = ByteSink()
        try source.fetch(reference, into: sink)
        let bytes = sink.bytes

        // Verify size, then digest — bad bytes must never become visible.
        guard UInt64(bytes.count) == reference.size else {
            throw FetchError.wrongSize(expected: reference.size, actual: UInt64(bytes.count))
        }
        let actual = ContentRef.forSha256(tenant: reference.tenant, bytes: bytes)
        guard actual.digest.lowercased() == reference.digest.lowercased() else {
            throw FetchError.wrongDigest(expected: reference.digest, actual: actual.digest)
        }

        lock.lock(); defer { lock.unlock() }
        if let rc = residentLocked(reference) { return rc } // lost a coalescing race
        try ensureRoomLocked(for: reference.size)
        objects[key] = bytes
        touchLocked(key)
        fetchedObjects &+= 1
        fetchedBytes &+= reference.size
        if inBatch {
            batchAdded.append(key)
            pinned.insert(key)
        }
        return ResidentContent(storageKey: key, size: reference.size)
    }

    public func materializeMissingBatch(_ references: [ContentRef]) throws -> [String] {
        var out: [String] = []
        for reference in references {
            do {
                out.append(try materializeMissing(reference).storageKey)
            } catch FetchError.io(let message) where message == "StorageFull" {
                break // ranked admission: stop, keep what fit
            }
        }
        return out
    }

    public func commitBatch() throws {
        lock.lock(); defer { lock.unlock() }
        // A directory fsync only happens when objects were actually linked in;
        // an empty batch (e.g. the empty restore on open) syncs nothing.
        if !batchAdded.isEmpty { directorySyncs &+= 1 }
        inBatch = false
        pinned = []
        batchAdded = []
    }

    public func rollbackBatch() throws {
        lock.lock(); defer { lock.unlock() }
        for key in batchAdded {
            objects[key] = nil
            lru.removeAll { $0 == key }
        }
        inBatch = false
        pinned = []
        batchAdded = []
    }

    public func openBytes(_ reference: ContentRef) throws -> [UInt8] {
        lock.lock(); defer { lock.unlock() }
        guard let bytes = objects[reference.storageKey] else {
            throw FetchError.notFound // "content is not resident"
        }
        touchLocked(reference.storageKey)
        return bytes
    }

    // MARK: - locked helpers

    private func usedLocked() -> UInt64 {
        objects.values.reduce(0) { $0 + UInt64($1.count) }
    }

    private func residentLocked(_ reference: ContentRef) -> ResidentContent? {
        guard let bytes = objects[reference.storageKey] else { return nil }
        return ResidentContent(storageKey: reference.storageKey, size: UInt64(bytes.count))
    }

    private func touchLocked(_ key: String) {
        lru.removeAll { $0 == key }
        lru.append(key)
    }

    private func ensureRoomLocked(for size: UInt64) throws {
        while usedLocked() + size > config.maxBytes {
            guard let victim = lru.first(where: { !pinned.contains($0) }) else {
                blocked &+= 1
                throw FetchError.io("StorageFull")
            }
            objects[victim] = nil
            lru.removeAll { $0 == victim }
            evicted &+= 1
        }
    }
}
