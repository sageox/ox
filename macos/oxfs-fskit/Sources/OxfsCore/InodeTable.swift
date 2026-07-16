import Foundation

/// The root directory always has inode 1.
public let ROOT_INODE: UInt64 = 1

/// Append-only stable path→inode allocator.
/// Port of `crates/oxfs/src/inode.rs::InodeTable`.
///
/// Removed paths keep their number so a later reappearance does not churn the
/// kernel's caches. Numbers are persisted to `inodes.v1` and made durable via
/// `sync()` before the namespace that references them becomes visible.
public final class InodeTable {
    private let handle: FileHandle
    private var byPath: [String: UInt64]
    private var next: UInt64
    private var dirty: Bool = false

    public init(stateDir: URL) throws {
        try FileManager.default.createDirectory(at: stateDir, withIntermediateDirectories: true)
        let url = stateDir.appendingPathComponent("inodes.v1")

        var byPath: [String: UInt64] = [:]
        var next: UInt64 = ROOT_INODE + 1

        if FileManager.default.fileExists(atPath: url.path) {
            let data = try Data(contentsOf: url)
            guard let text = String(data: data, encoding: .utf8) else {
                throw InodeError.notUTF8
            }
            for line in text.split(separator: "\n", omittingEmptySubsequences: true) {
                guard let tab = line.firstIndex(of: "\t") else {
                    throw InodeError.badRow
                }
                guard let number = UInt64(line[line.startIndex..<tab]) else {
                    throw InodeError.badNumber
                }
                let path = try InodeTable.unescape(String(line[line.index(after: tab)...]))
                if number <= ROOT_INODE || byPath.updateValue(number, forKey: path) != nil {
                    throw InodeError.duplicateRow
                }
                next = Swift.max(next, number &+ 1)
            }
        } else {
            FileManager.default.createFile(atPath: url.path, contents: nil)
        }

        self.handle = try FileHandle(forWritingTo: url)
        try self.handle.seekToEnd()
        self.byPath = byPath
        self.next = next
    }

    deinit { try? handle.close() }

    /// Return the stable inode for `path`, allocating (and appending) a new one
    /// on first sight. Empty path is the root.
    public func inode(for path: String) throws -> UInt64 {
        if path.isEmpty { return ROOT_INODE }
        if let number = byPath[path] { return number }

        let number = next
        let (bumped, overflow) = next.addingReportingOverflow(1)
        if overflow { throw InodeError.exhausted }
        next = bumped

        var row = Array("\(number)\t".utf8)
        row.append(contentsOf: InodeTable.escape(path))
        row.append(0x0a)
        try handle.write(contentsOf: Data(row))
        dirty = true
        byPath[path] = number
        return number
    }

    /// Make every inode allocated since the last sync durable.
    public func sync() throws {
        if dirty {
            try handle.synchronize()
            dirty = false
        }
    }

    // MARK: - escape/unescape (byte-faithful port)

    static func escape(_ value: String) -> [UInt8] {
        var out: [UInt8] = []
        for b in value.utf8 {
            switch b {
            case 0x25: out.append(contentsOf: Array("%25".utf8)) // %
            case 0x09: out.append(contentsOf: Array("%09".utf8)) // tab
            case 0x0a: out.append(contentsOf: Array("%0a".utf8)) // newline
            default: out.append(b)
            }
        }
        return out
    }

    static func unescape(_ value: String) throws -> String {
        let bytes = Array(value.utf8)
        var out: [UInt8] = []
        var i = 0
        while i < bytes.count {
            if bytes[i] != 0x25 {
                out.append(bytes[i])
                i += 1
                continue
            }
            if i + 2 >= bytes.count {
                throw InodeError.badEscape
            }
            guard let hi = hexValue(bytes[i + 1]), let lo = hexValue(bytes[i + 2]) else {
                throw InodeError.badEscape
            }
            out.append((hi << 4) | lo)
            i += 3
        }
        guard let str = String(bytes: out, encoding: .utf8) else {
            throw InodeError.notUTF8
        }
        return str
    }

    private static func hexValue(_ b: UInt8) -> UInt8? {
        switch b {
        case 0x30...0x39: return b - 0x30
        case 0x41...0x46: return b - 0x41 + 10
        case 0x61...0x66: return b - 0x61 + 10
        default: return nil
        }
    }
}

public enum InodeError: Error, Equatable {
    case badRow
    case badNumber
    case badEscape
    case notUTF8
    case duplicateRow
    case exhausted
}
