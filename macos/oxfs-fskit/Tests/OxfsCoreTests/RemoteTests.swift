import Foundation
import Testing
import zlib
@testable import OxfsCore

@Suite struct RemoteTests {
    final class MockTransport: RemoteHTTPTransport, @unchecked Sendable {
        private let lock = NSLock()
        private var responses: [(Data, Int)]
        private(set) var requests: [URLRequest] = []

        init(_ responses: [(Data, Int)]) { self.responses = responses }
        func request(_ request: URLRequest) throws -> (Data, HTTPURLResponse) {
            lock.lock(); defer { lock.unlock() }
            requests.append(request)
            let (data, status) = responses.removeFirst()
            return (data, HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: nil)!)
        }
    }

    private func repo() throws -> RemoteRepo {
        try RemoteRepo(
            virtualRoot: "teams/team_one",
            origin: URL(string: "https://git.test.sageox.ai/group/team-context.git")!
        )
    }

    @Test func registrySanitizesOriginsAndRejectsTraversal() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("oxfs-remote-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: root) }
        let git = root.appendingPathComponent("teams/team_one/.git")
        try FileManager.default.createDirectory(at: git, withIntermediateDirectories: true)
        try Data("[remote \"origin\"]\nurl = https://oauth2:secret@git.test.sageox.ai/group/team-context.git\n".utf8)
            .write(to: git.appendingPathComponent("config"))

        let registry = try RemoteRegistry.discover(host: "git.test.sageox.ai", endpointRoot: root)
        let (resolved, path) = try registry.resolve("teams/team_one/docs/README.md")
        #expect(path == "docs/README.md")
        #expect(resolved.origin.absoluteString == "https://git.test.sageox.ai/group/team-context.git")
        #expect(throws: RemoteError.invalidSelection) { _ = try registry.resolve("teams/team_one/../secret") }
    }

    @Test func parsesStrictLFSPointers() throws {
        let oid = String(repeating: "a", count: 64)
        #expect(try parseLFSPointer(Array("version https://git-lfs.github.com/spec/v1\noid sha256:\(oid)\nsize 42\n".utf8))
                == LFSPointer(oid: oid, size: 42))
        #expect(try parseLFSPointer(Array("ordinary".utf8)) == nil)
        #expect(throws: RemoteError.malformedLFSPointer) {
            _ = try parseLFSPointer(Array("version https://git-lfs.github.com/spec/v1\noid sha256:nope\nsize 42\n".utf8))
        }
    }

    @Test func rawFetchUsesEncodedGitLabPathAndStreamsBytes() throws {
        let transport = MockTransport([(Data("hello".utf8), 200)])
        let source = try RemoteContentSource(
            host: "git.test.sageox.ai",
            credentials: RemoteCredentials(username: "oauth2", password: "secret"),
            transport: transport
        )
        let reference = ContentRef.forSha256(tenant: "remote", bytes: Array("hello".utf8))
        source.register(reference, object: .raw(repo: try repo(), path: "docs/a b.md"))
        let sink = ByteSink(); try source.fetch(reference, into: sink)

        #expect(sink.bytes == Array("hello".utf8))
        #expect(transport.requests[0].url?.absoluteString.contains("projects/group%2Fteam-context") == true)
        #expect(transport.requests[0].url?.absoluteString.contains("files/docs%2Fa%20b.md/raw") == true)
        #expect(transport.requests[0].value(forHTTPHeaderField: "PRIVATE-TOKEN") == "secret")
        #expect(transport.requests[0].value(forHTTPHeaderField: "Authorization") == nil)
    }

    @Test func resolvesRawAndLFSPointerFilesIntoRegisteredContent() throws {
        let oid = String(repeating: "d", count: 64)
        let pointer = Data("version https://git-lfs.github.com/spec/v1\noid sha256:\(oid)\nsize 9\n".utf8)
        let transport = MockTransport([(Data("hello".utf8), 200), (pointer, 200)])
        let source = try RemoteContentSource(
            host: "git.test.sageox.ai",
            credentials: RemoteCredentials(username: "oauth2", password: "secret"), transport: transport
        )
        let registry = try RemoteRegistry(host: "git.test.sageox.ai", repositories: ["teams/team_one": try repo()])

        let raw = try source.resolveFile(registry: registry, selection: "teams/team_one/docs/a.txt", maxBytes: 100)
        #expect(raw.content == ContentRef.forSha256(tenant: "teams/team_one", bytes: Array("hello".utf8)))
        let sink = ByteSink(); try source.fetch(raw.content, into: sink)
        #expect(sink.bytes == Array("hello".utf8))

        let lfs = try source.resolveFile(registry: registry, selection: "teams/team_one/docs/b.bin", maxBytes: 100)
        #expect(lfs.content.digest == oid)
        #expect(lfs.content.size == 9)
    }

    @Test func directoryFallbackIsExplicitAndCapacityIsBounded() throws {
        let transport = MockTransport([(Data(), 404), (Data("too large".utf8), 200)])
        let source = try RemoteContentSource(
            host: "git.test.sageox.ai",
            credentials: RemoteCredentials(username: "oauth2", password: "secret"), transport: transport
        )
        let registry = try RemoteRegistry(host: "git.test.sageox.ai", repositories: ["teams/team_one": try repo()])
        #expect(throws: RemoteError.directoryRequiresArchive) {
            _ = try source.resolveFile(registry: registry, selection: "teams/team_one/docs", maxBytes: 100)
        }
        #expect(throws: RemoteError.selectionExceedsCapacity) {
            _ = try source.resolveFile(registry: registry, selection: "teams/team_one/large", maxBytes: 2)
        }
    }

    @Test func lfsBatchValidatesIdentityAndDownloadHost() throws {
        let oid = String(repeating: "b", count: 64)
        let batch = try JSONSerialization.data(withJSONObject: [
            "objects": [["oid": oid, "size": 3,
                         "actions": ["download": ["href": "https://git.test.sageox.ai/lfs/object",
                                                    "header": ["X-Test": "yes"]]]]]
        ])
        let transport = MockTransport([(batch, 200), (Data("lfs".utf8), 200)])
        let source = try RemoteContentSource(
            host: "git.test.sageox.ai",
            credentials: RemoteCredentials(username: "oauth2", password: "secret"), transport: transport
        )
        let reference = try ContentRef(tenant: "remote", algorithm: "sha256", digest: oid, size: 3)
        source.register(reference, object: .lfs(repo: try repo(), pointer: LFSPointer(oid: oid, size: 3)))
        let sink = ByteSink(); try source.fetch(reference, into: sink)
        #expect(sink.bytes == Array("lfs".utf8))
        #expect(transport.requests.count == 2)
        #expect(transport.requests[1].value(forHTTPHeaderField: "X-Test") == "yes")
    }

    @Test func lfsBatchRejectsCrossHostDownload() throws {
        let oid = String(repeating: "c", count: 64)
        let batch = try JSONSerialization.data(withJSONObject: [
            "objects": [["oid": oid, "size": 3,
                         "actions": ["download": ["href": "https://evil.example/object"]]]]
        ])
        let transport = MockTransport([(batch, 200)])
        let source = try RemoteContentSource(
            host: "git.test.sageox.ai",
            credentials: RemoteCredentials(username: "oauth2", password: "secret"), transport: transport
        )
        let reference = try ContentRef(tenant: "remote", algorithm: "sha256", digest: oid, size: 3)
        source.register(reference, object: .lfs(repo: try repo(), pointer: LFSPointer(oid: oid, size: 3)))
        #expect(throws: RemoteError.invalidLFSResponse) { try source.fetch(reference, into: ByteSink()) }
    }

    @Test func archiveExpandsSelectedRegularFilesAndRejectsLinks() throws {
        let archive = try gzip(makeTar([
            ("team-HEAD/docs/a.txt", UInt8(ascii: "0"), Data("hello".utf8)),
            ("team-HEAD/docs/sub/b.txt", UInt8(ascii: "0"), Data("world".utf8)),
        ]))
        let entries = try unpackRemoteArchive(archive, selected: "docs", maxBytes: 10)
        #expect(entries.map(\.path) == ["docs/a.txt", "docs/sub/b.txt"])
        #expect(entries.map(\.bytes) == [Data("hello".utf8), Data("world".utf8)])

        let linked = try gzip(makeTar([("team-HEAD/docs/link", UInt8(ascii: "2"), Data())]))
        #expect(throws: RemoteError.archiveContainsLinkOrSpecialFile) {
            _ = try unpackRemoteArchive(linked, selected: "docs", maxBytes: 10)
        }
        #expect(throws: RemoteError.selectionExceedsCapacity) {
            _ = try unpackRemoteArchive(archive, selected: "docs", maxBytes: 9)
        }
    }

    @Test func resolvesDirectoryArchiveIntoRegisteredContent() throws {
        let archive = try gzip(makeTar([
            ("team-HEAD/docs/a.txt", UInt8(ascii: "0"), Data("hello".utf8)),
        ]))
        let transport = MockTransport([(archive, 200)])
        let source = try RemoteContentSource(
            host: "git.test.sageox.ai",
            credentials: RemoteCredentials(username: "oauth2", password: "secret"), transport: transport
        )
        let registry = try RemoteRegistry(host: "git.test.sageox.ai", repositories: ["teams/team_one": try repo()])
        let files = try source.resolveDirectory(
            registry: registry, selection: "teams/team_one/docs", maxBytes: 10
        )
        #expect(files.map(\.virtualPath) == ["teams/team_one/docs/a.txt"])
        let sink = ByteSink(); try source.fetch(files[0].content, into: sink)
        #expect(sink.bytes == Array("hello".utf8))
        #expect(transport.requests[0].url?.absoluteString.contains("archive.tar.gz") == true)
        #expect(transport.requests[0].url?.query?.contains("path=docs") == true)
    }

    private func makeTar(_ entries: [(String, UInt8, Data)]) -> Data {
        var tar = Data()
        for (path, type, bytes) in entries {
            var header = [UInt8](repeating: 0, count: 512)
            write(path, into: &header, range: 0..<100)
            writeOctal(0o444, into: &header, range: 100..<108)
            writeOctal(0, into: &header, range: 108..<116)
            writeOctal(0, into: &header, range: 116..<124)
            writeOctal(UInt64(bytes.count), into: &header, range: 124..<136)
            writeOctal(42, into: &header, range: 136..<148)
            for i in 148..<156 { header[i] = UInt8(ascii: " ") }
            header[156] = type
            write("ustar", into: &header, range: 257..<263)
            let checksum = header.reduce(UInt64(0)) { $0 + UInt64($1) }
            writeOctal(checksum, into: &header, range: 148..<156)
            tar.append(contentsOf: header); tar.append(bytes)
            tar.append(Data(repeating: 0, count: (512 - bytes.count % 512) % 512))
        }
        tar.append(Data(repeating: 0, count: 1024))
        return tar
    }

    private func write(_ value: String, into bytes: inout [UInt8], range: Range<Int>) {
        for (index, byte) in value.utf8.prefix(range.count).enumerated() { bytes[range.lowerBound + index] = byte }
    }

    private func writeOctal(_ value: UInt64, into bytes: inout [UInt8], range: Range<Int>) {
        let value = String(value, radix: 8)
        let padded = String(repeating: "0", count: Swift.max(0, range.count - value.count - 1)) + value + "\0"
        write(padded, into: &bytes, range: range)
    }

    private func gzip(_ input: Data) throws -> Data {
        var stream = z_stream()
        guard deflateInit2_(&stream, Z_DEFAULT_COMPRESSION, Z_DEFLATED, 16 + MAX_WBITS, 8,
                           Z_DEFAULT_STRATEGY, ZLIB_VERSION, Int32(MemoryLayout<z_stream>.size)) == Z_OK else {
            throw RemoteError.invalidArchive
        }
        defer { deflateEnd(&stream) }
        var output = Data()
        let status: Int32 = input.withUnsafeBytes { source in
            stream.next_in = UnsafeMutablePointer<Bytef>(mutating: source.bindMemory(to: Bytef.self).baseAddress)
            stream.avail_in = uInt(source.count)
            var result = Z_OK
            var chunk = [UInt8](repeating: 0, count: 4096)
            repeat {
                result = chunk.withUnsafeMutableBytes { destination in
                    stream.next_out = destination.bindMemory(to: Bytef.self).baseAddress
                    stream.avail_out = uInt(destination.count)
                    return deflate(&stream, Z_FINISH)
                }
                output.append(contentsOf: chunk[0..<(chunk.count - Int(stream.avail_out))])
            } while result == Z_OK
            return result
        }
        guard status == Z_STREAM_END else { throw RemoteError.invalidArchive }
        return output
    }
}
