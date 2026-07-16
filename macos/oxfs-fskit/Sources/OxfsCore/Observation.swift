import Foundation

/// Port of `crates/oxfs/src/observations.rs`.
///
/// Append-only access log written synchronously on the read path: every
/// open/read/dismiss appends one JSONL record to `state/access.jsonl`. This is
/// oxFS's per-read observability surface.
public enum ObservationKind: String, Sendable {
    case open
    case read
    case dismiss
}

public final class ObservationLog {
    private let handle: FileHandle
    private let lock = NSLock()
    /// Injected clock so tests are deterministic; defaults to wall-clock ms.
    private let nowMillis: @Sendable () -> UInt64

    public init(stateDir: URL, nowMillis: @escaping @Sendable () -> UInt64 = {
        UInt64(Date().timeIntervalSince1970 * 1000)
    }) throws {
        try FileManager.default.createDirectory(at: stateDir, withIntermediateDirectories: true)
        let url = stateDir.appendingPathComponent("access.jsonl")
        if !FileManager.default.fileExists(atPath: url.path) {
            FileManager.default.createFile(atPath: url.path, contents: nil)
        }
        self.handle = try FileHandle(forWritingTo: url)
        try self.handle.seekToEnd()
        self.nowMillis = nowMillis
    }

    deinit { try? handle.close() }

    public func append(_ kind: ObservationKind, inode: UInt64, path: String, offset: UInt64, bytes: Int) throws {
        let line = "{\"ts_ms\":\(nowMillis()),\"kind\":\"\(kind.rawValue)\",\"inode\":\(inode),\"path\":\(Self.jsonString(path)),\"offset\":\(offset),\"bytes\":\(bytes)}\n"
        lock.lock()
        defer { lock.unlock() }
        try handle.write(contentsOf: Data(line.utf8))
    }

    /// Minimal JSON string escaping for the `path` field.
    static func jsonString(_ value: String) -> String {
        var out = "\""
        for scalar in value.unicodeScalars {
            switch scalar {
            case "\"": out += "\\\""
            case "\\": out += "\\\\"
            case "\n": out += "\\n"
            case "\r": out += "\\r"
            case "\t": out += "\\t"
            default:
                if scalar.value < 0x20 {
                    out += String(format: "\\u%04x", scalar.value)
                } else {
                    out.unicodeScalars.append(scalar)
                }
            }
        }
        out += "\""
        return out
    }
}
