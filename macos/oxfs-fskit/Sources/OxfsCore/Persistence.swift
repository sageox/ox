import Foundation

// Codable conformances live in each type's own file (Swift requires that for
// synthesis). `RelativePath`'s custom coder is in Path.swift.

/// Port of `crates/oxfs/src/selections.rs::SelectionStore` (JSON form).
///
/// Persists the per-session desired manifests so a reopened Workspace restores
/// the same working set (and re-materializes it, up to the cache cap).
public final class SelectionStore {
    private let url: URL
    private let pendingURL: URL

    public init(stateDir: URL) throws {
        try FileManager.default.createDirectory(at: stateDir, withIntermediateDirectories: true)
        self.url = stateDir.appendingPathComponent("selections.json")
        self.pendingURL = stateDir.appendingPathComponent("selections.pending.json")
    }

    public func load() -> [String: Manifest]? {
        let source = FileManager.default.fileExists(atPath: pendingURL.path) ? pendingURL : url
        guard let data = try? Data(contentsOf: source) else { return nil }
        return try? JSONDecoder().decode([String: Manifest].self, from: data)
    }

    public func replace(_ sessions: [String: Manifest]) throws {
        let data = try JSONEncoder().encode(sessions)
        try durableAtomicReplace(data, at: url)
    }

    /// Durably record the intended selection set before cache reconciliation.
    /// Recovery rolls this intent forward, closing the crash window between the
    /// cache transaction and `selections.json`.
    public func stage(_ sessions: [String: Manifest]) throws {
        try durableAtomicReplace(try JSONEncoder().encode(sessions), at: pendingURL)
    }

    public func commitStaged() throws {
        guard FileManager.default.fileExists(atPath: pendingURL.path) else { return }
        if rename(pendingURL.path, url.path) != 0 {
            throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
        }
        try syncDirectory(url.deletingLastPathComponent())
    }

    public func rollbackStaged() throws {
        guard unlink(pendingURL.path) != 0 else {
            try syncDirectory(url.deletingLastPathComponent())
            return
        }
        guard errno == ENOENT else {
            throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
        }
    }
}

public func durableAtomicReplace(_ data: Data, at url: URL) throws {
    let directory = url.deletingLastPathComponent()
    let tmp = directory.appendingPathComponent(".\(url.lastPathComponent).\(UUID().uuidString).tmp")
    FileManager.default.createFile(atPath: tmp.path, contents: nil)
    do {
        let handle = try FileHandle(forWritingTo: tmp)
        do {
            try handle.write(contentsOf: data)
            try handle.synchronize()
            try handle.close()
        } catch {
            try? handle.close()
            throw error
        }
        if rename(tmp.path, url.path) != 0 {
            throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
        }
        try syncDirectory(directory)
    } catch {
        try? FileManager.default.removeItem(at: tmp)
        throw error
    }
}

private func syncDirectory(_ directory: URL) throws {
    let fd = open(directory.path, O_RDONLY)
    guard fd >= 0 else {
        throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
    }
    defer { close(fd) }
    guard fsync(fd) == 0 else {
        throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
    }
}
