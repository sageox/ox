import Foundation
import CryptoKit
import OxfsCore

public struct MountConfiguration: Codable, Sendable {
    public let sourceBookmark: Data
    public let state: String
    public let cacheBytes: UInt64

    public init(sourceBookmark: Data, state: String = ".", cacheBytes: UInt64) {
        self.sourceBookmark = sourceBookmark
        self.state = state
        self.cacheBytes = cacheBytes
    }

    public static let fileName = ".oxfs-mount.json"

    public static func load(from resourceRoot: URL) throws -> MountConfiguration {
        try JSONDecoder().decode(
            Self.self,
            from: Data(contentsOf: resourceRoot.appendingPathComponent(fileName))
        )
    }

    public func write(to url: URL) throws {
        try JSONEncoder().encode(self).write(to: url, options: .atomic)
    }

    public static func bookmark(for source: URL) throws -> Data {
        try source.bookmarkData(options: [.withSecurityScope], includingResourceValuesForKeys: nil, relativeTo: nil)
    }

    func sourceURL() throws -> URL {
        var stale = false
        let url = try URL(
            resolvingBookmarkData: sourceBookmark,
            options: [.withSecurityScope, .withoutUI],
            relativeTo: nil,
            bookmarkDataIsStale: &stale
        )
        guard !stale else { throw CocoaError(.fileReadUnknown) }
        return url.standardizedFileURL.resolvingSymlinksInPath()
    }

    func stateURL(relativeTo root: URL) throws -> URL {
        try Self.confinedURL(state, relativeTo: root)
    }

    public static func identifierUUID(for resourceRoot: URL) -> UUID {
        let canonical = resourceRoot.standardizedFileURL.resolvingSymlinksInPath().path
        var bytes = Array(SHA256.hash(data: Data(canonical.utf8)).prefix(16))
        bytes[6] = (bytes[6] & 0x0f) | 0x50
        bytes[8] = (bytes[8] & 0x3f) | 0x80
        return UUID(uuid: (
            bytes[0], bytes[1], bytes[2], bytes[3], bytes[4], bytes[5], bytes[6], bytes[7],
            bytes[8], bytes[9], bytes[10], bytes[11], bytes[12], bytes[13], bytes[14], bytes[15]
        ))
    }

    private static func confinedURL(_ path: String, relativeTo root: URL) throws -> URL {
        guard !path.isEmpty, !NSString(string: path).isAbsolutePath else {
            throw POSIXError(.EINVAL)
        }
        let root = root.standardizedFileURL.resolvingSymlinksInPath()
        let candidate = root.appendingPathComponent(path).standardizedFileURL.resolvingSymlinksInPath()
        let prefix = root.path.hasSuffix("/") ? root.path : root.path + "/"
        guard candidate.path == root.path || candidate.path.hasPrefix(prefix) else {
            throw POSIXError(.EPERM)
        }
        return candidate
    }
}

/// Rebuilds the local content map before Workspace recovery. The mount config
/// names a source root; persisted selections still contain the authoritative
/// manifests and only need digest-to-file resolution here.
final class LocalDirectorySource: ContentSource, @unchecked Sendable {
    private let files: [String: URL]
    private let securityScopedRoot: URL

    init(root: URL) throws {
        guard root.startAccessingSecurityScopedResource() else { throw POSIXError(.EACCES) }
        securityScopedRoot = root
        var succeeded = false
        defer {
            if !succeeded { root.stopAccessingSecurityScopedResource() }
        }
        var found: [String: URL] = [:]
        let keys: Set<URLResourceKey> = [.isRegularFileKey]
        guard let walker = FileManager.default.enumerator(
            at: root, includingPropertiesForKeys: Array(keys),
            options: [.skipsHiddenFiles, .skipsPackageDescendants]
        ) else { throw CocoaError(.fileReadNoSuchFile) }
        for case let file as URL in walker {
            guard try file.resourceValues(forKeys: keys).isRegularFile == true else { continue }
            let bytes = [UInt8](try Data(contentsOf: file))
            let reference = ContentRef.forSha256(tenant: "local", bytes: bytes)
            found[reference.digest] = file
        }
        files = found
        succeeded = true
    }

    deinit { securityScopedRoot.stopAccessingSecurityScopedResource() }

    func fetch(_ reference: ContentRef, into output: ContentWriter) throws {
        guard let file = files[reference.digest] else { throw FetchError.notFound }
        let handle = try FileHandle(forReadingFrom: file)
        defer { try? handle.close() }
        while let data = try handle.read(upToCount: 1024 * 1024), !data.isEmpty {
            try output.write([UInt8](data))
        }
    }
}
