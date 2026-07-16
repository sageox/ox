import Foundation

// Codable conformances live in each type's own file (Swift requires that for
// synthesis). `RelativePath`'s custom coder is in Path.swift.

/// Port of `crates/oxfs/src/selections.rs::SelectionStore` (JSON form).
///
/// Persists the per-session desired manifests so a reopened Workspace restores
/// the same working set (and re-materializes it, up to the cache cap).
public final class SelectionStore {
    private let url: URL

    public init(stateDir: URL) throws {
        try FileManager.default.createDirectory(at: stateDir, withIntermediateDirectories: true)
        self.url = stateDir.appendingPathComponent("selections.json")
    }

    public func load() -> [String: Manifest]? {
        guard let data = try? Data(contentsOf: url) else { return nil }
        return try? JSONDecoder().decode([String: Manifest].self, from: data)
    }

    public func replace(_ sessions: [String: Manifest]) throws {
        let data = try JSONEncoder().encode(sessions)
        // Atomic replace so a crash never leaves a half-written selection file.
        let tmp = url.appendingPathExtension("tmp")
        try data.write(to: tmp, options: .atomic)
        _ = try FileManager.default.replaceItemAt(url, withItemAt: tmp)
    }
}
