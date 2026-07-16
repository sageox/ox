import Testing
@testable import OxfsCore

@Suite struct ManifestTests {
    private func ref(_ s: String) -> ContentRef {
        ContentRef.forSha256(tenant: "t", bytes: Array(s.utf8))
    }

    private func entry(_ path: String, mode: UInt32 = 0o644) throws -> ManifestEntry {
        try ManifestEntry(
            path: path, sourceID: "s", sourceKind: "local",
            mode: mode, mtimeSecs: 0, content: ref(path), reason: "test"
        )
    }

    @Test func modeIsMaskedToReadExecute() throws {
        let e = try entry("a/b.txt", mode: 0o777)
        #expect(e.mode == 0o555)
    }

    @Test func validAcceptsDistinctPaths() throws {
        let m = Manifest(sessionID: "sess", generation: 1, entries: [
            try entry("a/b.txt"), try entry("a/c.txt"), try entry("d.txt"),
        ])
        #expect(throws: Never.self) { try m.validate() }
    }

    @Test func rejectsEmptySession() throws {
        let m = Manifest(sessionID: "", generation: 1, entries: [try entry("a.txt")])
        #expect(throws: ManifestError.missingSession) { try m.validate() }
    }

    @Test func rejectsDuplicatePath() throws {
        let m = Manifest(sessionID: "s", generation: 1, entries: [
            try entry("a/b.txt"), try entry("a/b.txt"),
        ])
        #expect(throws: ManifestError.duplicate("a/b.txt")) { try m.validate() }
    }

    @Test func rejectsFileDirectoryCollision() throws {
        // "a" is a file, "a/b" tries to treat "a" as a directory.
        let m = Manifest(sessionID: "s", generation: 1, entries: [
            try entry("a"), try entry("a/b"),
        ])
        #expect(throws: ManifestError.collision("a")) { try m.validate() }
    }
}
