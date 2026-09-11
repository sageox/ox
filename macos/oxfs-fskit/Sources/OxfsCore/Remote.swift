import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

private let lfsPointerVersion = "version https://git-lfs.github.com/spec/v1"
private let maxLFSPointerBytes = 200

public struct RemoteRepo: Sendable, Equatable {
    public let virtualRoot: String
    public let origin: URL
    public let ref: String

    public init(virtualRoot: String, origin: URL, ref: String = "HEAD") throws {
        guard !virtualRoot.isEmpty,
              !ref.isEmpty,
              !ref.contains(".."),
              !ref.contains("\0"),
              origin.scheme == "https",
              origin.user == nil,
              origin.password == nil,
              origin.query == nil,
              origin.fragment == nil else {
            throw RemoteError.untrustedOrigin
        }
        self.virtualRoot = virtualRoot
        self.origin = origin
        self.ref = ref
    }
}

public struct RemoteRegistry: Sendable {
    public let host: String
    private let repositories: [String: RemoteRepo]

    public init(host: String, repositories: [String: RemoteRepo]) throws {
        _ = try Self.endpoint(forGitHost: host)
        self.host = host
        self.repositories = repositories
    }

    public static func discover(host: String, dataRoot: URL? = nil) throws -> RemoteRegistry {
        let endpoint = try endpoint(forGitHost: host)
        let base = dataRoot ?? defaultDataRoot()
        return try discover(host: host, endpointRoot: base.appendingPathComponent(endpoint))
    }

    public static func discover(host: String, endpointRoot: URL) throws -> RemoteRegistry {
        _ = try endpoint(forGitHost: host)
        var repositories: [String: RemoteRepo] = [:]
        for category in ["ledgers", "teams", "kb"] {
            let root = endpointRoot.appendingPathComponent(category)
            let entries = (try? FileManager.default.contentsOfDirectory(
                at: root, includingPropertiesForKeys: [.isDirectoryKey]
            )) ?? []
            for entry in entries where (try? entry.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) == true {
                guard let configured = try readOrigin(entry.appendingPathComponent(".git/config")),
                      let sanitized = sanitizeOrigin(configured, expectedHost: host) else { continue }
                let virtualRoot = "\(category)/\(entry.lastPathComponent)"
                repositories[virtualRoot] = try RemoteRepo(
                    virtualRoot: virtualRoot,
                    origin: sanitized,
                    ref: readCheckoutRef(entry.appendingPathComponent(".git")) ?? "HEAD"
                )
            }
        }
        return try RemoteRegistry(host: host, repositories: repositories)
    }

    public func resolve(_ selection: String) throws -> (RemoteRepo, String) {
        let components = try safeComponents(selection)
        guard components.count >= 3 else { throw RemoteError.invalidSelection }
        let root = components[0...1].joined(separator: "/")
        guard let repo = repositories[root] else { throw RemoteError.repositoryNotFound }
        return (repo, components.dropFirst(2).joined(separator: "/"))
    }

    public var repos: [RemoteRepo] { repositories.values.sorted { $0.virtualRoot < $1.virtualRoot } }

    private static func endpoint(forGitHost host: String) throws -> String {
        switch host {
        case "git.sageox.ai": "sageox.ai"
        case "git.test.sageox.ai": "test.sageox.ai"
        default: throw RemoteError.unsupportedHost
        }
    }

    private static func defaultDataRoot() -> URL {
        let env = ProcessInfo.processInfo.environment
        if let xdg = env["XDG_DATA_HOME"] { return URL(fileURLWithPath: xdg).appendingPathComponent("sageox") }
        return FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".local/share/sageox")
    }
}

public struct LFSPointer: Sendable, Equatable, Codable {
    public let oid: String
    public let size: UInt64
}

public func parseLFSPointer(_ bytes: [UInt8]) throws -> LFSPointer? {
    guard bytes.count <= maxLFSPointerBytes,
          let text = String(bytes: bytes, encoding: .utf8) else { return nil }
    var lines = text.split(whereSeparator: \ .isNewline).map(String.init)
    guard lines.first == lfsPointerVersion else { return nil }
    lines.removeFirst()
    var oid: String?
    var size: UInt64?
    for line in lines {
        if line.hasPrefix("oid sha256:") {
            let value = String(line.dropFirst("oid sha256:".count)).lowercased()
            if value.count == 64 && value.utf8.allSatisfy(\.isAsciiHexDigit) { oid = value }
        } else if line.hasPrefix("size ") {
            size = UInt64(line.dropFirst("size ".count)).flatMap { $0 > 0 ? $0 : nil }
        }
    }
    guard let oid, let size else { throw RemoteError.malformedLFSPointer }
    return LFSPointer(oid: oid, size: size)
}

public struct RemoteCredentials: Sendable, Equatable {
    public let username: String
    public let password: String

    public init(username: String, password: String) {
        self.username = username
        self.password = password
    }
}

public protocol RemoteCredentialProvider: Sendable {
    func credentials(host: String) throws -> RemoteCredentials
}

public struct OxCredentialProvider: RemoteCredentialProvider {
    public init() {}
    public func credentials(host: String) throws -> RemoteCredentials {
        guard host == "git.sageox.ai" || host == "git.test.sageox.ai" else { throw RemoteError.unsupportedHost }
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/env")
        process.arguments = ["ox", "git-credential-helper", "get"]
        let input = Pipe(); let output = Pipe(); let errors = Pipe()
        process.standardInput = input; process.standardOutput = output; process.standardError = errors
        try process.run()
        try input.fileHandleForWriting.write(contentsOf: Data("protocol=https\nhost=\(host)\n\n".utf8))
        try input.fileHandleForWriting.close()
        process.waitUntilExit()
        guard process.terminationStatus == 0 else { throw RemoteError.credentialsUnavailable }
        let text = String(decoding: output.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
        var username = "oauth2"; var password: String?
        for line in text.split(whereSeparator: \ .isNewline) {
            if line.hasPrefix("username=") { username = String(line.dropFirst("username=".count)) }
            if line.hasPrefix("password=") { password = String(line.dropFirst("password=".count)) }
        }
        guard let password, !password.isEmpty else { throw RemoteError.credentialsUnavailable }
        return RemoteCredentials(username: username, password: password)
    }
}

public protocol RemoteHTTPTransport: Sendable {
    func request(_ request: URLRequest) throws -> (Data, HTTPURLResponse)
}

public final class URLSessionRemoteTransport: RemoteHTTPTransport, @unchecked Sendable {
    private final class NoRedirectDelegate: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
        func urlSession(
            _ session: URLSession,
            task: URLSessionTask,
            willPerformHTTPRedirection response: HTTPURLResponse,
            newRequest request: URLRequest,
            completionHandler: @escaping @Sendable (URLRequest?) -> Void
        ) { completionHandler(nil) }
    }

    private final class ResultBox: @unchecked Sendable {
        let lock = NSLock()
        var result: Result<(Data, HTTPURLResponse), Error>?
    }

    private let session: URLSession
    private let delegate: NoRedirectDelegate
    public init() {
        let delegate = NoRedirectDelegate()
        self.delegate = delegate
        self.session = URLSession(configuration: .ephemeral, delegate: delegate, delegateQueue: nil)
    }

    public func request(_ request: URLRequest) throws -> (Data, HTTPURLResponse) {
        let box = ResultBox(); let done = DispatchSemaphore(value: 0)
        let task = session.dataTask(with: request) { data, response, error in
            let result: Result<(Data, HTTPURLResponse), Error>
            if let error { result = .failure(error) }
            else if let response = response as? HTTPURLResponse { result = .success((data ?? Data(), response)) }
            else { result = .failure(RemoteError.invalidHTTPResponse) }
            box.lock.lock(); box.result = result; box.lock.unlock(); done.signal()
        }
        task.resume(); done.wait()
        box.lock.lock(); defer { box.lock.unlock() }
        return try box.result!.get()
    }
}

public enum RemoteObject: Sendable {
    case inline(Data)
    case raw(repo: RemoteRepo, path: String)
    case lfs(repo: RemoteRepo, pointer: LFSPointer)
}

public struct RemoteResolvedFile: Sendable, Equatable {
    public let virtualPath: String
    public let repoPath: String
    public let content: ContentRef
    public let mode: UInt32
    public let mtimeSecs: UInt64
}

public final class RemoteContentSource: ContentSource, @unchecked Sendable {
    private let host: String
    private let credentials: RemoteCredentials
    private let transport: RemoteHTTPTransport
    private let lock = NSLock()
    private var objects: [ContentRef: RemoteObject] = [:]

    public init(host: String, credentials: RemoteCredentials, transport: RemoteHTTPTransport) throws {
        guard host == "git.sageox.ai" || host == "git.test.sageox.ai" else { throw RemoteError.unsupportedHost }
        self.host = host
        self.credentials = credentials
        self.transport = transport
    }

    public func register(_ reference: ContentRef, object: RemoteObject) {
        lock.lock(); defer { lock.unlock() }
        objects[reference] = object
    }

    /// Resolve a single GitLab path. A 404 means the selection may be a
    /// directory; archive expansion is deliberately a separate bounded parser.
    public func resolveFile(
        registry: RemoteRegistry,
        selection: String,
        maxBytes: UInt64
    ) throws -> RemoteResolvedFile {
        let (repo, path) = try registry.resolve(selection)
        try requireTrusted(repo)
        let request = try rawRequest(repo: repo, path: path)
        let (data, response) = try transport.request(request)
        guard response.url?.scheme == request.url?.scheme,
              response.url?.host == request.url?.host else { throw RemoteError.untrustedRedirect }
        if response.statusCode == 404 { throw RemoteError.directoryRequiresArchive }
        guard (200..<300).contains(response.statusCode) else {
            throw RemoteError.http(operation: "raw fetch", status: response.statusCode)
        }
        let content: ContentRef
        if let pointer = try parseLFSPointer([UInt8](data)) {
            content = try ContentRef(tenant: repo.virtualRoot, algorithm: "sha256", digest: pointer.oid, size: pointer.size)
            guard pointer.size <= maxBytes else { throw RemoteError.selectionExceedsCapacity }
            register(content, object: .lfs(repo: repo, pointer: pointer))
        } else {
            guard UInt64(data.count) <= maxBytes else { throw RemoteError.selectionExceedsCapacity }
            content = ContentRef.forSha256(tenant: repo.virtualRoot, bytes: [UInt8](data))
            register(content, object: .inline(data))
        }
        return RemoteResolvedFile(
            virtualPath: selection, repoPath: path, content: content,
            mode: 0o444, mtimeSecs: 0
        )
    }

    public func resolveDirectory(
        registry: RemoteRegistry,
        selection: String,
        maxBytes: UInt64
    ) throws -> [RemoteResolvedFile] {
        let (repo, path) = try registry.resolve(selection)
        try requireTrusted(repo)
        var project = repo.origin.path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        if project.hasSuffix(".git") { project.removeLast(4) }
        var components = URLComponents(string: "https://\(host)/api/v4/projects/\(percentEncodePathComponent(project))/repository/archive.tar.gz")!
        components.queryItems = [URLQueryItem(name: "sha", value: repo.ref),
                                 URLQueryItem(name: "path", value: path),
                                 URLQueryItem(name: "include_lfs_blobs", value: "false")]
        guard let url = components.url else { throw RemoteError.invalidURL }
        let archive = try perform(apiRequest(url), operation: "archive fetch")
        var logicalBytes: UInt64 = 0
        var pending: [(RemoteResolvedFile, RemoteObject)] = []
        for entry in try unpackRemoteArchive(archive, selected: path, maxBytes: maxBytes) {
            let content: ContentRef
            let object: RemoteObject
            if let pointer = entry.pointer {
                content = try ContentRef(tenant: repo.virtualRoot, algorithm: "sha256", digest: pointer.oid, size: pointer.size)
                object = .lfs(repo: repo, pointer: pointer)
            } else {
                content = ContentRef.forSha256(tenant: repo.virtualRoot, bytes: [UInt8](entry.bytes))
                object = .inline(entry.bytes)
            }
            let (nextLogicalBytes, overflow) = logicalBytes.addingReportingOverflow(content.size)
            guard !overflow, nextLogicalBytes <= maxBytes else { throw RemoteError.selectionExceedsCapacity }
            logicalBytes = nextLogicalBytes
            pending.append((RemoteResolvedFile(
                virtualPath: "\(repo.virtualRoot)/\(entry.path)", repoPath: entry.path,
                content: content, mode: entry.mode, mtimeSecs: entry.mtimeSecs
            ), object))
        }
        for (file, object) in pending { register(file.content, object: object) }
        return pending.map(\.0)
    }

    public func fetch(_ reference: ContentRef, into output: ContentWriter) throws {
        lock.lock(); let object = objects[reference]; lock.unlock()
        guard let object else { throw FetchError.notFound }
        let data: Data
        switch object {
        case let .inline(bytes):
            data = bytes
        case let .raw(repo, path):
            try requireTrusted(repo)
            data = try perform(try rawRequest(repo: repo, path: path), operation: "raw fetch")
        case let .lfs(repo, pointer):
            try requireTrusted(repo)
            data = try fetchLFS(repo: repo, pointer: pointer)
        }
        for offset in stride(from: 0, to: data.count, by: 64 * 1024) {
            try output.write([UInt8](data[offset..<Swift.min(offset + 64 * 1024, data.count)]))
        }
    }

    private func rawRequest(repo: RemoteRepo, path: String) throws -> URLRequest {
        var project = repo.origin.path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        if project.hasSuffix(".git") { project.removeLast(4) }
        let encodedProject = percentEncodePathComponent(project)
        let encodedPath = percentEncodePathComponent(path)
        var components = URLComponents(string: "https://\(host)/api/v4/projects/\(encodedProject)/repository/files/\(encodedPath)/raw")!
        components.queryItems = [
            URLQueryItem(name: "ref", value: repo.ref),
            URLQueryItem(name: "lfs", value: "false")
        ]
        guard let url = components.url else { throw RemoteError.invalidURL }
        return apiRequest(url)
    }

    private func fetchLFS(repo: RemoteRepo, pointer: LFSPointer) throws -> Data {
        let batchURL = ["info", "lfs", "objects", "batch"].reduce(repo.origin) {
            $0.appendingPathComponent($1)
        }
        let body: [String: Any] = ["operation": "download", "transfers": ["basic"],
                                  "objects": [["oid": pointer.oid, "size": pointer.size]]]
        var request = basicRequest(batchURL)
        request.httpMethod = "POST"
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        request.setValue("application/vnd.git-lfs+json", forHTTPHeaderField: "Accept")
        request.setValue("application/vnd.git-lfs+json", forHTTPHeaderField: "Content-Type")
        let response = try perform(request, operation: "LFS batch")
        guard let json = try JSONSerialization.jsonObject(with: response) as? [String: Any],
              let objects = json["objects"] as? [[String: Any]], let object = objects.first,
              object["oid"] as? String == pointer.oid,
              (object["size"] as? NSNumber)?.uint64Value == pointer.size,
              let actions = object["actions"] as? [String: Any],
              let download = actions["download"] as? [String: Any],
              let href = download["href"] as? String, let url = URL(string: href),
              url.scheme == "https", url.host == batchURL.host else { throw RemoteError.invalidLFSResponse }
        var downloadRequest = basicRequest(url)
        if let headers = download["header"] as? [String: String] {
            for (name, value) in headers { downloadRequest.setValue(value, forHTTPHeaderField: name) }
        }
        return try perform(downloadRequest, operation: "LFS download")
    }

    private func apiRequest(_ url: URL) -> URLRequest {
        var request = URLRequest(url: url, timeoutInterval: 300)
        request.setValue(credentials.password, forHTTPHeaderField: "PRIVATE-TOKEN")
        request.setValue("oxfs/0.1", forHTTPHeaderField: "User-Agent")
        return request
    }

    private func basicRequest(_ url: URL) -> URLRequest {
        var request = URLRequest(url: url, timeoutInterval: 300)
        let token = Data("\(credentials.username):\(credentials.password)".utf8).base64EncodedString()
        request.setValue("Basic \(token)", forHTTPHeaderField: "Authorization")
        request.setValue("oxfs/0.1", forHTTPHeaderField: "User-Agent")
        return request
    }

    private func perform(_ request: URLRequest, operation: String) throws -> Data {
        let (data, response) = try transport.request(request)
        guard response.url?.scheme == request.url?.scheme,
              response.url?.host == request.url?.host else { throw RemoteError.untrustedRedirect }
        guard (200..<300).contains(response.statusCode) else {
            throw RemoteError.http(operation: operation, status: response.statusCode)
        }
        return data
    }

    private func requireTrusted(_ repo: RemoteRepo) throws {
        guard repo.origin.scheme == "https", repo.origin.host == host else { throw RemoteError.untrustedOrigin }
    }
}

public enum RemoteError: Error, Equatable {
    case unsupportedHost, untrustedOrigin, invalidSelection, repositoryNotFound
    case malformedLFSPointer, invalidURL, invalidLFSResponse, invalidHTTPResponse, untrustedRedirect
    case credentialsUnavailable, directoryRequiresArchive, selectionExceedsCapacity
    case invalidArchive, archiveContainsLinkOrSpecialFile, archiveEscapedSelection, archiveDuplicatePath
    case http(operation: String, status: Int)
}

private func safeComponents(_ value: String) throws -> [String] {
    guard !value.isEmpty, !value.hasPrefix("/") else { throw RemoteError.invalidSelection }
    let parts = value.split(separator: "/", omittingEmptySubsequences: false).map(String.init)
    guard parts.allSatisfy({ !$0.isEmpty && $0 != "." && $0 != ".." }) else { throw RemoteError.invalidSelection }
    return parts
}

private func readOrigin(_ config: URL) throws -> String? {
    guard let text = try? String(contentsOf: config, encoding: .utf8) else { return nil }
    var inOrigin = false
    for raw in text.split(whereSeparator: \ .isNewline) {
        let line = raw.trimmingCharacters(in: .whitespaces)
        if line.hasPrefix("[") { inOrigin = line == "[remote \"origin\"]"; continue }
        if inOrigin, let equals = line.firstIndex(of: "=") {
            let key = line[..<equals].trimmingCharacters(in: .whitespaces)
            if key == "url" { return String(line[line.index(after: equals)...]).trimmingCharacters(in: .whitespaces) }
        }
    }
    return nil
}

private func readCheckoutRef(_ gitDirectory: URL) -> String? {
    guard let text = try? String(
        contentsOf: gitDirectory.appendingPathComponent("HEAD"),
        encoding: .utf8
    ) else { return nil }
    let value = text.trimmingCharacters(in: .whitespacesAndNewlines)
    let prefix = "ref: refs/heads/"
    guard value.hasPrefix(prefix) else { return nil }
    let ref = String(value.dropFirst(prefix.count))
    guard !ref.isEmpty,
          !ref.contains(".."),
          !ref.contains("\0"),
          !ref.hasPrefix("/"),
          !ref.hasSuffix("/") else { return nil }
    return ref
}

private func sanitizeOrigin(_ value: String, expectedHost: String) -> URL? {
    guard var components = URLComponents(string: value), components.scheme == "https",
          components.host == expectedHost else { return nil }
    components.user = nil; components.password = nil; components.query = nil; components.fragment = nil
    return components.url
}

private func percentEncodePathComponent(_ value: String) -> String {
    let allowed = CharacterSet.alphanumerics.union(CharacterSet(charactersIn: "-._~"))
    return value.addingPercentEncoding(withAllowedCharacters: allowed) ?? ""
}
