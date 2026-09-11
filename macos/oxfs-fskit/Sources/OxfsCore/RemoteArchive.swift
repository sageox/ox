import Foundation
import zlib

struct RemoteArchiveEntry {
    let path: String
    let bytes: Data
    let mode: UInt32
    let mtimeSecs: UInt64
    let pointer: LFSPointer?
}

func unpackRemoteArchive(
    _ compressed: Data,
    selected: String,
    maxBytes: UInt64
) throws -> [RemoteArchiveEntry] {
    let tar = try gunzip(compressed, limit: maxBytes.saturatingAdd(16 * 1024 * 1024))
    var entries: [RemoteArchiveEntry] = []
    var seen = Set<String>()
    var expanded: UInt64 = 0
    var offset = 0
    while offset + 512 <= tar.count {
        let header = tar.subdata(in: offset..<offset + 512)
        if header.allSatisfy({ $0 == 0 }) { break }
        try validateTarChecksum(header)
        let name = try tarString(header, 0..<100)
        let prefix = try tarString(header, 345..<500)
        let archivedPath = prefix.isEmpty ? name : "\(prefix)/\(name)"
        let components = try safeArchiveComponents(archivedPath)
        guard components.count >= 2 else { throw RemoteError.invalidArchive }
        let relative = components.dropFirst().joined(separator: "/")
        let type = header[156]
        let size = try tarOctal(header, 124..<136)
        let mode = try tarOctal(header, 100..<108)
        let mtime = try tarOctal(header, 136..<148)
        offset += 512
        guard size <= UInt64(Int.max), offset + Int(size) <= tar.count else { throw RemoteError.invalidArchive }
        let padded = ((Int(size) + 511) / 512) * 512
        guard offset + padded <= tar.count else { throw RemoteError.invalidArchive }
        defer { offset += padded }
        if type == UInt8(ascii: "5") { continue }
        guard type == 0 || type == UInt8(ascii: "0") else { throw RemoteError.archiveContainsLinkOrSpecialFile }
        guard relative == selected || relative.hasPrefix(selected + "/") else { throw RemoteError.archiveEscapedSelection }
        guard seen.insert(relative).inserted else { throw RemoteError.archiveDuplicatePath }
        let (nextExpanded, overflow) = expanded.addingReportingOverflow(size)
        guard !overflow, nextExpanded <= maxBytes else { throw RemoteError.selectionExceedsCapacity }
        expanded = nextExpanded
        let bytes = tar.subdata(in: offset..<offset + Int(size))
        entries.append(RemoteArchiveEntry(
            path: relative, bytes: bytes, mode: UInt32(mode) & 0o555,
            mtimeSecs: mtime, pointer: try parseLFSPointer([UInt8](bytes))
        ))
    }
    guard !entries.isEmpty else { throw RemoteError.repositoryNotFound }
    return entries.sorted { $0.path < $1.path }
}

private func gunzip(_ compressed: Data, limit: UInt64) throws -> Data {
    var stream = z_stream()
    guard inflateInit2_(&stream, 16 + MAX_WBITS, ZLIB_VERSION, Int32(MemoryLayout<z_stream>.size)) == Z_OK else {
        throw RemoteError.invalidArchive
    }
    defer { inflateEnd(&stream) }
    var output = Data()
    let status: Int32 = compressed.withUnsafeBytes { input in
        stream.next_in = UnsafeMutablePointer<Bytef>(mutating: input.bindMemory(to: Bytef.self).baseAddress)
        stream.avail_in = uInt(input.count)
        var result = Z_OK
        var chunk = [UInt8](repeating: 0, count: 64 * 1024)
        repeat {
            result = chunk.withUnsafeMutableBytes { bytes in
                stream.next_out = bytes.bindMemory(to: Bytef.self).baseAddress
                stream.avail_out = uInt(bytes.count)
                return inflate(&stream, Z_NO_FLUSH)
            }
            let produced = chunk.count - Int(stream.avail_out)
            output.append(contentsOf: chunk[0..<produced])
            if UInt64(output.count) > limit { return Z_MEM_ERROR }
        } while result == Z_OK
        return result
    }
    guard status == Z_STREAM_END else {
        if status == Z_MEM_ERROR { throw RemoteError.selectionExceedsCapacity }
        throw RemoteError.invalidArchive
    }
    return output
}

private func tarString(_ data: Data, _ range: Range<Int>) throws -> String {
    let bytes = data[range].prefix { $0 != 0 }
    guard let value = String(bytes: bytes, encoding: .utf8) else { throw RemoteError.invalidArchive }
    return value
}

private func tarOctal(_ data: Data, _ range: Range<Int>) throws -> UInt64 {
    let text = try tarString(data, range).trimmingCharacters(in: .whitespacesAndNewlines)
    guard text.isEmpty || text.allSatisfy({ ("0"..."7").contains($0) }),
          let value = UInt64(text.isEmpty ? "0" : text, radix: 8) else { throw RemoteError.invalidArchive }
    return value
}

private func validateTarChecksum(_ header: Data) throws {
    let expected = try tarOctal(header, 148..<156)
    var actual: UInt64 = 0
    for index in 0..<header.count {
        actual += UInt64((148..<156).contains(index) ? UInt8(ascii: " ") : header[index])
    }
    guard actual == expected else { throw RemoteError.invalidArchive }
}

private func safeArchiveComponents(_ path: String) throws -> [String] {
    guard !path.hasPrefix("/") else { throw RemoteError.archiveEscapedSelection }
    let parts = path.split(separator: "/", omittingEmptySubsequences: false).map(String.init)
    guard parts.allSatisfy({ !$0.isEmpty && $0 != "." && $0 != ".." }) else {
        throw RemoteError.archiveEscapedSelection
    }
    return parts
}

private extension UInt64 {
    func saturatingAdd(_ other: UInt64) -> UInt64 {
        let (result, overflow) = addingReportingOverflow(other)
        return overflow ? .max : result
    }
}
