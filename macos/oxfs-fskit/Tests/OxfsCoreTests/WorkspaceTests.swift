import Testing
import Foundation
@testable import OxfsCore

/// Ported behavioral scenarios from `crates/oxfs/tests/workspace.rs`. These are
/// the parity oracle: each asserts a durable behavior oxFS users rely on.
@Suite struct WorkspaceTests {
    // MARK: - test doubles

    /// Mirrors the Rust `MemorySource`: fetch keyed by content digest.
    final class MemorySource: ContentSource, @unchecked Sendable {
        let objects: [String: [UInt8]]
        init(_ objects: [String: [UInt8]]) { self.objects = objects }
        func fetch(_ reference: ContentRef, into output: ContentWriter) throws {
            guard let bytes = objects[reference.digest] else { throw FetchError.notFound }
            try output.write(bytes)
        }
    }

    private func tempRoot(_ name: String) -> URL {
        FileManager.default.temporaryDirectory
            .appendingPathComponent("oxfs-\(name)-\(UUID().uuidString)")
    }

    // sha256("abc") — the canonical reference used across scenarios.
    private func referenceAbc() -> ContentRef {
        try! ContentRef(tenant: "t", algorithm: "sha256",
                        digest: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", size: 3)
    }
    // sha256("xyz")
    private func referenceXyz() -> ContentRef {
        try! ContentRef(tenant: "t", algorithm: "sha256",
                        digest: "3608bca1e44ea6c4d268eb6db02260269892c0b42b86bbf1e77a6fa16c3c9282", size: 3)
    }

    private func source(_ bytes: [UInt8]) -> MemorySource {
        MemorySource([referenceAbc().digest: bytes])
    }

    private func manifest(_ generation: UInt64) throws -> Manifest {
        Manifest(sessionID: "s1", generation: generation, entries: [
            try ManifestEntry(path: "sessions/one/raw.jsonl", sourceID: "src", sourceKind: "Session",
                              mode: 0o644, mtimeSecs: 123, content: referenceAbc(), reason: "related"),
        ])
    }

    // MARK: - scenarios

    /// Port of `wysiwyg_and_stable_restart`: a path is absent until materialized,
    /// then readable, and its inode is stable across a reopen.
    @Test func wysiwygAndStableRestart() throws {
        let root = tempRoot("restart")
        defer { try? FileManager.default.removeItem(at: root) }

        var ws = try Workspace.open(root: root, source: source(Array("abc".utf8)))
        #expect(ws.snapshot().byPath("sessions/one/raw.jsonl") == nil)
        _ = try ws.apply(manifest(1))
        let before = try #require(ws.snapshot().byPath("sessions/one/raw.jsonl")).inode
        #expect(try ws.openInode(before).read(offset: 0, count: 99) == Array("abc".utf8))

        // Reopen: same source, must re-materialize and reuse the same inode.
        ws = try Workspace.open(root: root, source: source(Array("abc".utf8)))
        #expect(try #require(ws.snapshot().byPath("sessions/one/raw.jsonl")).inode == before)
    }

    /// Port of `stale_generation_is_ignored`.
    @Test func staleGenerationIsIgnored() throws {
        let root = tempRoot("stale")
        defer { try? FileManager.default.removeItem(at: root) }
        let ws = try Workspace.open(root: root, source: source(Array("abc".utf8)))
        #expect(try ws.apply(manifest(2)).applied)
        #expect(try ws.apply(manifest(1)).applied == false)
    }

    /// Port of `invalid_bytes_never_become_visible`: content whose bytes don't
    /// hash to the reference digest is rejected and never appears.
    @Test func invalidBytesNeverBecomeVisible() throws {
        let root = tempRoot("invalid")
        defer { try? FileManager.default.removeItem(at: root) }
        let ws = try Workspace.open(root: root, source: source(Array("abd".utf8))) // wrong bytes
        #expect(throws: (any Error).self) { _ = try ws.apply(self.manifest(1)) }
        #expect(ws.snapshot().byPath("sessions/one/raw.jsonl") == nil)
    }

    /// Port of `sqlite_catalog_is_persistent_and_telemetry_is_observable`
    /// (the cache-implementation-agnostic subset — catalog/sqlite specifics are Phase 3).
    @Test func fetchAndResidencyTelemetryObservable() throws {
        let root = tempRoot("telemetry")
        defer { try? FileManager.default.removeItem(at: root) }
        let ws = try Workspace.open(root: root, source: source(Array("abc".utf8)))
        _ = try ws.apply(manifest(1))
        let t = try ws.cacheTelemetry()
        #expect(t.fetchedObjects == 1)
        #expect(t.fetchedBytes == 3)
        #expect(t.residentObjects == 1)
        #expect(t.residentBytes == 3)
        #expect(t.directorySyncs == 1)
    }

    /// Port of `manifest_batch_materializes_unique_objects_and_syncs_directory_once`.
    @Test func batchMaterializesUniqueObjectsAndSyncsOnce() throws {
        let root = tempRoot("batch")
        defer { try? FileManager.default.removeItem(at: root) }
        var objects: [String: [UInt8]] = [:]
        var entries: [ManifestEntry] = []
        for i in 0..<64 {
            let bytes = Array("object-\(i)".utf8)
            let ref = ContentRef.forSha256(tenant: "t", bytes: bytes)
            objects[ref.digest] = bytes
            entries.append(try ManifestEntry(path: "objects/\(i)", sourceID: "src", sourceKind: "Session",
                                             mode: 0o444, mtimeSecs: 0, content: ref, reason: "batch"))
        }
        let ws = try Workspace.open(root: root, source: MemorySource(objects))
        _ = try ws.apply(Manifest(sessionID: "batch", generation: 1, entries: entries))
        let t = try ws.cacheTelemetry()
        #expect(t.fetchedObjects == 64)
        #expect(t.residentObjects == 64)
        #expect(t.directorySyncs == 1)
    }

    /// Port of `synthetic_index_is_resident_and_aggregates_selectors`:
    /// `.sageox/INDEX.{md,json}` are always present and describe the working set.
    @Test func syntheticIndexIsResidentAndAggregates() throws {
        let root = tempRoot("index")
        defer { try? FileManager.default.removeItem(at: root) }
        let ws = try Workspace.open(root: root, source: source(Array("abc".utf8)))
        _ = try ws.apply(manifest(1))
        let snap = ws.snapshot()
        let jsonInode = try #require(snap.byPath(".sageox/INDEX.json")).inode
        let mdInode = try #require(snap.byPath(".sageox/INDEX.md")).inode
        let json = String(bytes: try ws.openInode(jsonInode).read(offset: 0, count: Int.max), encoding: .utf8)!
        let md = String(bytes: try ws.openInode(mdInode).read(offset: 0, count: Int.max), encoding: .utf8)!
        #expect(json.contains("\"path\":\"sessions/one/raw.jsonl\""))
        #expect(json.contains("\"status\":\"available\""))
        #expect(md.contains("oxFS working set"))
    }

    /// Port of `conflicting_session_content_is_skipped_per_entry_not_rejected`:
    /// a canonical-path collision is handled per entry — the resident file is
    /// never overwritten, the rest of the update applies, and the losing selector
    /// is recorded as `path_collision`.
    @Test func conflictingSessionContentSkippedPerEntry() throws {
        let root = tempRoot("conflict")
        defer { try? FileManager.default.removeItem(at: root) }
        let objects: [String: [UInt8]] = [
            referenceAbc().digest: Array("abc".utf8),
            referenceXyz().digest: Array("xyz".utf8),
        ]
        let ws = try Workspace.open(root: root, source: MemorySource(objects))

        _ = try ws.apply(Manifest(sessionID: "a", generation: 1, entries: [
            try ManifestEntry(path: "same", sourceID: "a", sourceKind: "Session",
                              mode: 0o444, mtimeSecs: 0, content: referenceAbc(), reason: "first"),
        ]))
        let inode = try #require(ws.snapshot().byPath("same")).inode

        _ = try ws.apply(Manifest(sessionID: "b", generation: 1, entries: [
            try ManifestEntry(path: "same", sourceID: "b", sourceKind: "Session",
                              mode: 0o444, mtimeSecs: 0, content: referenceXyz(), reason: "second"),
            try ManifestEntry(path: "other", sourceID: "b", sourceKind: "Session",
                              mode: 0o444, mtimeSecs: 0, content: referenceXyz(), reason: "kept"),
        ]))

        // Resident file untouched — still "abc", not "xyz".
        #expect(try ws.openInode(inode).read(offset: 0, count: 3) == Array("abc".utf8))
        #expect(ws.snapshot().byPath("same") != nil)
        // The rest of the update still applied.
        #expect(ws.snapshot().byPath("other") != nil)
        // Selector statuses recorded in the index.
        let jsonInode = try #require(ws.snapshot().byPath(".sageox/INDEX.json")).inode
        let json = String(bytes: try ws.openInode(jsonInode).read(offset: 0, count: Int.max), encoding: .utf8)!
        #expect(json.contains("path_collision"))
        #expect(json.contains("\"session_id\":\"a\",\"reason\":\"first\",\"status\":\"available\""))
        #expect(json.contains("\"session_id\":\"b\",\"reason\":\"second\",\"status\":\"path_collision\""))
    }
}
