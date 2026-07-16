/// A single selected path in a session's manifest.
/// Port of `crates/oxfs/src/manifest.rs::ManifestEntry`.
public struct ManifestEntry: Sendable, Codable {
    public let path: RelativePath
    public let sourceID: String
    public let sourceKind: String
    public let mode: UInt32
    public let mtimeSecs: UInt64
    public let content: ContentRef
    public let reason: String

    public init(
        path: String,
        sourceID: String,
        sourceKind: String,
        mode: UInt32,
        mtimeSecs: UInt64,
        content: ContentRef,
        reason: String
    ) throws {
        self.path = try RelativePath.parse(path)
        self.sourceID = sourceID
        self.sourceKind = sourceKind
        self.mode = mode & 0o555 // read/execute bits only — oxFS is read-only
        self.mtimeSecs = mtimeSecs
        self.content = content
        self.reason = reason
    }
}

/// A session's complete set of selected paths for one generation.
/// Port of `crates/oxfs/src/manifest.rs::Manifest`.
public struct Manifest: Sendable, Codable {
    public let sessionID: String
    public let generation: UInt64
    public let entries: [ManifestEntry]

    public init(sessionID: String, generation: UInt64, entries: [ManifestEntry]) {
        self.sessionID = sessionID
        self.generation = generation
        self.entries = entries
    }

    /// Reject empty sessions, duplicate paths, and file/directory collisions.
    /// Faithful port of Rust `Manifest::validate`.
    public func validate() throws {
        if sessionID.isEmpty {
            throw ManifestError.missingSession
        }
        var paths = Set<String>()
        var files = Set<String>()
        for entry in entries {
            if !paths.insert(entry.path.asString).inserted {
                throw ManifestError.duplicate(entry.path.asString)
            }
            var prefix = ""
            let parts = entry.path.components()
            for (index, part) in parts.enumerated() {
                if !prefix.isEmpty { prefix.append("/") }
                prefix.append(part)
                // A non-final component that is already a file → collision.
                if index + 1 != parts.count && files.contains(prefix) {
                    throw ManifestError.collision(prefix)
                }
            }
            files.insert(entry.path.asString)
        }
        // A file that is a strict prefix directory of another path → collision.
        for file in files {
            if paths.contains(where: { $0.hasPrefix("\(file)/") }) {
                throw ManifestError.collision(file)
            }
        }
    }
}

/// Port of Rust `enum ManifestError`.
public enum ManifestError: Error, Equatable {
    case missingSession
    case duplicate(String)
    case collision(String)
}
