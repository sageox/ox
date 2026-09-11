import Testing
import Foundation
@testable import OxfsCore

/// Phase 3 disk-cache conformance: crash-safe SQLite catalog persistence,
/// same-size-corruption healing across restart, and legacy metadata migration.
/// Ported from the disk-specific scenarios in `crates/oxfs/tests/workspace.rs`.
@Suite struct DiskCacheTests {
    final class MemorySource: ContentSource, @unchecked Sendable {
        let objects: [String: [UInt8]]
        init(_ objects: [String: [UInt8]]) { self.objects = objects }
        func fetch(_ reference: ContentRef, into output: ContentWriter) throws {
            guard let bytes = objects[reference.digest] else { throw FetchError.notFound }
            try output.write(bytes)
        }
    }

    final class CountingSource: ContentSource, @unchecked Sendable {
        private let lock = NSLock()
        private var count = 0
        let bytes: [UInt8]

        init(_ bytes: [UInt8]) { self.bytes = bytes }

        func fetch(_ reference: ContentRef, into output: ContentWriter) throws {
            lock.lock()
            count += 1
            lock.unlock()
            Thread.sleep(forTimeInterval: 0.02)
            try output.write(bytes)
        }

        var fetchCount: Int {
            lock.lock()
            defer { lock.unlock() }
            return count
        }
    }

    final class ErrorBox: @unchecked Sendable {
        private let lock = NSLock()
        private var values: [Error] = []

        func append(_ error: Error) {
            lock.lock()
            values.append(error)
            lock.unlock()
        }

        var isEmpty: Bool {
            lock.lock()
            defer { lock.unlock() }
            return values.isEmpty
        }
    }

    private func tempRoot(_ n: String) -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("oxfs-disk-\(n)-\(UUID().uuidString)")
    }
    private func referenceAbc() -> ContentRef {
        try! ContentRef(tenant: "t", algorithm: "sha256",
                        digest: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", size: 3)
    }
    private func referenceXyz() -> ContentRef {
        ContentRef.forSha256(tenant: "t", bytes: Array("xyz".utf8))
    }
    private func source(_ bytes: [UInt8]) -> MemorySource { MemorySource([referenceAbc().digest: bytes]) }
    private func manifest(_ g: UInt64) throws -> Manifest {
        Manifest(sessionID: "s1", generation: g, entries: [
            try ManifestEntry(path: "sessions/one/raw.jsonl", sourceID: "src", sourceKind: "Session",
                              mode: 0o644, mtimeSecs: 123, content: referenceAbc(), reason: "related"),
        ])
    }
    private func walkFiles(_ dir: URL) -> [URL] {
        guard let e = FileManager.default.enumerator(at: dir, includingPropertiesForKeys: [.isRegularFileKey]) else { return [] }
        return e.compactMap { $0 as? URL }.filter {
            (try? $0.resourceValues(forKeys: [.isRegularFileKey]).isRegularFile) == true
        }
    }
    private func findObject(_ cacheDir: URL) -> URL? {
        walkFiles(cacheDir.appendingPathComponent("objects")).first { $0.lastPathComponent.hasPrefix("sha256-") }
    }

    /// Port of `sqlite_catalog_is_persistent_and_telemetry_is_observable`.
    @Test func catalogIsPersistentAndTelemetryObservable() throws {
        let root = tempRoot("catalog")
        defer { try? FileManager.default.removeItem(at: root) }
        let fm = FileManager.default

        var ws = try Workspace.open(root: root, source: source(Array("abc".utf8)))
        _ = try ws.apply(manifest(1))
        let t = try ws.cacheTelemetry()
        #expect(t.fetchedObjects == 1)
        #expect(t.fetchedBytes == 3)
        #expect(t.residentObjects == 1)
        #expect(t.residentBytes == 3)
        #expect(t.directorySyncs == 1)
        #expect(t.catalogTransactions == 2) // empty restore + generation 1

        let cache = root.appendingPathComponent("cache")
        #expect(fm.fileExists(atPath: cache.appendingPathComponent("catalog.sqlite").path))
        #expect(!fm.fileExists(atPath: cache.appendingPathComponent("metadata.v1").path))
        #expect(!fm.fileExists(atPath: cache.appendingPathComponent("active-apply.v1").path))

        // Reopen: residency survives restart.
        ws = try Workspace.open(root: root, source: source(Array("abc".utf8)))
        #expect(try ws.cacheTelemetry().residentObjects == 1)
    }

    /// Port of `same_size_corruption_is_rejected_after_restart`: a same-length
    /// on-disk corruption is caught by re-verification on reopen and healed
    /// (re-fetched), so the file still reads its true bytes.
    @Test func sameSizeCorruptionHealsAfterRestart() throws {
        let root = tempRoot("corrupt")
        defer { try? FileManager.default.removeItem(at: root) }

        var ws = try Workspace.open(root: root, source: source(Array("abc".utf8)))
        _ = try ws.apply(manifest(1))

        let cache = root.appendingPathComponent("cache")
        let object = try #require(findObject(cache))
        try Data("abd".utf8).write(to: object) // same size, wrong bytes

        ws = try Workspace.open(root: root, source: source(Array("abc".utf8)))
        let inode = try #require(ws.snapshot().byPath("sessions/one/raw.jsonl")).inode
        #expect(try ws.openInode(inode).read(offset: 0, count: 3) == Array("abc".utf8))
    }

    @Test func sameProcessCorruptionIsNeverServed() throws {
        let root = tempRoot("same-process-corrupt")
        defer { try? FileManager.default.removeItem(at: root) }

        let ws = try Workspace.open(root: root, source: source(Array("abc".utf8)))
        _ = try ws.apply(manifest(1))
        let inode = try #require(ws.snapshot().byPath("sessions/one/raw.jsonl")).inode
        #expect(try ws.openInode(inode).read(offset: 0, count: 3) == Array("abc".utf8))

        let object = try #require(findObject(root.appendingPathComponent("cache")))
        try Data("abd".utf8).write(to: object)
        #expect(throws: (any Error).self) {
            _ = try ws.openInode(inode)
        }
        #expect(try ws.cacheTelemetry().residentObjects == 0)
    }

    @Test func injectedCommitFailureRollsBackCandidate() throws {
        let root = tempRoot("commit-rollback")
        defer { try? FileManager.default.removeItem(at: root) }
        let cache = try DiskContentCache(
            root: root.appendingPathComponent("cache"),
            source: source(Array("abc".utf8))
        )
        let ws = try Workspace.open(root: root, cache: cache)
        cache.failNextCommit()

        #expect(throws: (any Error).self) {
            _ = try ws.apply(self.manifest(1))
        }
        #expect(ws.snapshot().byPath("sessions/one/raw.jsonl") == nil)
        #expect(try ws.cacheTelemetry().residentObjects == 0)
        #expect(!FileManager.default.fileExists(
            atPath: root.appendingPathComponent("cache/active-apply.v1").path
        ))
    }

    @Test func openDescriptorPinsExactObjectUntilClose() throws {
        let root = tempRoot("open-pin")
        defer { try? FileManager.default.removeItem(at: root) }
        let abc = referenceAbc(), xyz = referenceXyz()
        let cache = try DiskContentCache(
            root: root.appendingPathComponent("cache"),
            source: MemorySource([
                abc.digest: Array("abc".utf8),
                xyz.digest: Array("xyz".utf8),
            ]),
            config: CacheConfig(maxBytes: 3)
        )
        _ = try cache.materialize(abc)
        let opened = try cache.openContent(abc)

        #expect(throws: (any Error).self) { _ = try cache.materialize(xyz) }
        #expect(try opened.read(offset: 0, count: 3) == Array("abc".utf8))
        #expect(try cache.resident(abc) != nil)

        try opened.close()
        _ = try cache.materialize(xyz)
        #expect(try cache.resident(abc) == nil)
        #expect(try cache.resident(xyz) != nil)
    }

    @Test func concurrentMaterializationCoalescesOneOriginFetch() throws {
        let root = tempRoot("coalesced-fetch")
        defer { try? FileManager.default.removeItem(at: root) }
        let reference = referenceAbc()
        let source = CountingSource(Array("abc".utf8))
        let cache = try DiskContentCache(
            root: root.appendingPathComponent("cache"),
            source: source
        )
        let errors = ErrorBox()

        DispatchQueue.concurrentPerform(iterations: 16) { _ in
            do {
                _ = try cache.materialize(reference)
            } catch {
                errors.append(error)
            }
        }

        #expect(errors.isEmpty)
        #expect(source.fetchCount == 1)
        #expect(try cache.telemetry().fetchedObjects == 1)
        #expect(try cache.openBytes(reference) == Array("abc".utf8))
    }

    /// Port of `legacy_metadata_migrates_without_scanning_the_object_tree`.
    @Test func legacyMetadataMigrates() throws {
        let root = tempRoot("migrate")
        defer { try? FileManager.default.removeItem(at: root) }
        let fm = FileManager.default

        var ws = try Workspace.open(root: root, source: source(Array("abc".utf8)))
        _ = try ws.apply(manifest(1))

        let cache = root.appendingPathComponent("cache")
        let object = try #require(findObject(cache))
        // key = path relative to cache/objects (e.g. "<tenanthash>/sha256-<digest>")
        let objectsDir = cache.appendingPathComponent("objects").path + "/"
        let key = String(object.path.dropFirst(objectsDir.count))

        for suffix in ["catalog.sqlite", "catalog.sqlite-wal", "catalog.sqlite-shm"] {
            try? fm.removeItem(at: cache.appendingPathComponent(suffix))
        }
        try Data("\(key)\t3\t7\n".utf8).write(to: cache.appendingPathComponent("metadata.v1"))

        ws = try Workspace.open(root: root, source: source(Array("abc".utf8)))
        #expect(try ws.cacheTelemetry().residentObjects == 1)
        #expect(!fm.fileExists(atPath: cache.appendingPathComponent("metadata.v1").path))
        #expect(fm.fileExists(atPath: cache.appendingPathComponent("catalog.sqlite").path))
    }
}
