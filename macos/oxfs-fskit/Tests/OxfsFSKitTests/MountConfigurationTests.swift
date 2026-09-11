import Foundation
import Testing
@testable import OxfsFSKit

@Suite("FSKit mount configuration")
struct MountConfigurationTests {
    @Test("resource identity is stable and resource-specific")
    func resourceIdentity() throws {
        let root = URL(fileURLWithPath: "/tmp/oxfs-resource-a")
        #expect(MountConfiguration.identifierUUID(for: root) == MountConfiguration.identifierUUID(for: root))
        #expect(MountConfiguration.identifierUUID(for: root) != MountConfiguration.identifierUUID(
            for: URL(fileURLWithPath: "/tmp/oxfs-resource-b")
        ))
    }

    @Test("state remains beneath the resource")
    func confinedPaths() throws {
        let root = FileManager.default.temporaryDirectory
            .appendingPathComponent(UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }

        let valid = MountConfiguration(sourceBookmark: Data(), state: ".oxfs-state", cacheBytes: 1)
        #expect(try valid.stateURL(relativeTo: root).path.hasPrefix(root.path + "/"))

        let traversal = MountConfiguration(sourceBookmark: Data(), state: "../outside", cacheBytes: 1)
        #expect(throws: (any Error).self) { try traversal.stateURL(relativeTo: root) }

        let absolute = MountConfiguration(sourceBookmark: Data(), state: "/tmp/outside", cacheBytes: 1)
        #expect(throws: (any Error).self) { try absolute.stateURL(relativeTo: root) }
    }

    @Test("symlinks cannot escape the security-scoped resource")
    func symlinkEscape() throws {
        let fm = FileManager.default
        let root = fm.temporaryDirectory.appendingPathComponent(UUID().uuidString, isDirectory: true)
        let outside = fm.temporaryDirectory.appendingPathComponent(UUID().uuidString, isDirectory: true)
        try fm.createDirectory(at: root, withIntermediateDirectories: true)
        try fm.createDirectory(at: outside, withIntermediateDirectories: true)
        defer {
            try? fm.removeItem(at: root)
            try? fm.removeItem(at: outside)
        }
        try fm.createSymbolicLink(at: root.appendingPathComponent("escape"), withDestinationURL: outside)

        let config = MountConfiguration(sourceBookmark: Data(), state: "escape", cacheBytes: 1)
        #expect(throws: (any Error).self) { try config.stateURL(relativeTo: root) }
    }
}
