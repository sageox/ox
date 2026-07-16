import Testing
import Foundation
@testable import OxfsCore

@Suite struct InodeTableTests {
    private func tempDir() -> URL {
        FileManager.default.temporaryDirectory
            .appendingPathComponent("oxfs-inodes-\(UUID().uuidString)")
    }

    /// Port of Rust `inode::tests::inode_survives_reopen`.
    @Test func inodeSurvivesReopen() throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }

        let table = try InodeTable(stateDir: dir)
        let first = try table.inode(for: "a/b")
        try table.sync()

        let reopened = try InodeTable(stateDir: dir)
        let second = try reopened.inode(for: "a/b")
        #expect(first == second)
    }

    @Test func emptyPathIsRoot() throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let table = try InodeTable(stateDir: dir)
        #expect(try table.inode(for: "") == ROOT_INODE)
    }

    @Test func allocatesMonotonicallyAndReusesWithinSession() throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let table = try InodeTable(stateDir: dir)
        let a = try table.inode(for: "a")
        let b = try table.inode(for: "b")
        #expect(b == a + 1)
        #expect(try table.inode(for: "a") == a) // stable within session
    }

    @Test func escapeRoundTripsSpecialBytes() throws {
        // % , tab, newline must survive a round trip through the log encoding.
        let tricky = "a\tb%c\nd"
        let escaped = InodeTable.escape(tricky)
        #expect(!escaped.contains(0x09) && !escaped.contains(0x0a))
        #expect(try InodeTable.unescape(String(bytes: escaped, encoding: .utf8)!) == tricky)
    }
}
