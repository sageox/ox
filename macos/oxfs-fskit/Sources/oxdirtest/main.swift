import Foundation
import CryptoKit
import OxfsCore

// oxdirtest — interactive human-selected-directory harness, Swift port of the
// Workspace-driving control loop in `crates/oxfs/src/oxdirtest.rs`.
//
// Commands: select DIR..., clear, status, metrics, help, quit. Each select/clear
// indexes the chosen directories under --source (hash every file), publishes a
// manifest to a real Workspace (disk-backed verified cache), and reports what
// materialized. The FSKit mount/browse half awaits Phase 7 (needs code signing);
// everything else — selection, verified materialization, eviction, metrics — is
// exercised here exactly as it would be behind the mount.

// MARK: - Local directory content source (digest -> file)

final class LocalDirectorySource: ContentSource, @unchecked Sendable {
    private let lock = NSLock()
    private var byDigest: [String: URL] = [:]

    func replace(_ map: [String: URL]) -> [String: URL] {
        lock.lock(); defer { lock.unlock() }
        let previous = byDigest
        byDigest = map
        return previous
    }

    func fetch(_ reference: ContentRef, into output: ContentWriter) throws {
        lock.lock(); let path = byDigest[reference.digest]; lock.unlock()
        guard let path else { throw FetchError.notFound }
        let handle = try FileHandle(forReadingFrom: path)
        defer { try? handle.close() }
        while let chunk = try handle.read(upToCount: 65536), !chunk.isEmpty {
            try output.write([UInt8](chunk))
        }
    }
}

// MARK: - indexing

struct Indexed { var entries: [ManifestEntry]; var sources: [String: URL]; var files: Int; var bytes: UInt64 }

func hexDigest(_ d: SHA256.Digest) -> String { d.map { String(format: "%02x", $0) }.joined() }

/// Stream-hash a file into a validated ContentRef (tenant "oxdirtest").
func hashFile(_ url: URL) throws -> ContentRef {
    let handle = try FileHandle(forReadingFrom: url)
    defer { try? handle.close() }
    var hasher = SHA256(); var size: UInt64 = 0
    while let chunk = try handle.read(upToCount: 65536), !chunk.isEmpty {
        hasher.update(data: chunk); size &+= UInt64(chunk.count)
    }
    return try ContentRef(tenant: "oxdirtest", algorithm: "sha256", digest: hexDigest(hasher.finalize()), size: size)
}

/// Recursively collect regular files under `path`, sorted by name; skip
/// symlinks and special files (mirrors `collect_files`).
func collectFiles(_ path: URL, into files: inout [URL]) throws {
    let fm = FileManager.default
    let attrs = try fm.attributesOfItem(atPath: path.path)
    let type = attrs[.type] as? FileAttributeType
    if type == .typeSymbolicLink { FileHandle.standardError.write(Data("oxdirtest: skipping symlink \(path.path)\n".utf8)); return }
    if type == .typeRegular { files.append(path); return }
    guard type == .typeDirectory else { FileHandle.standardError.write(Data("oxdirtest: skipping special file \(path.path)\n".utf8)); return }
    let children = try fm.contentsOfDirectory(at: path, includingPropertiesForKeys: nil).sorted { $0.lastPathComponent < $1.lastPathComponent }
    for child in children { try collectFiles(child, into: &files) }
}

func indexFiles(sourceRoot: URL, selections: [String]) throws -> Indexed {
    var files: [URL] = []
    for selection in selections { try collectFiles(sourceRoot.appendingPathComponent(selection), into: &files) }
    files = Array(Set(files)).sorted { $0.path < $1.path }

    var entries: [ManifestEntry] = []
    var sources: [String: URL] = [:]
    let rootPrefix = sourceRoot.path.hasSuffix("/") ? sourceRoot.path : sourceRoot.path + "/"
    for file in files {
        guard file.path.hasPrefix(rootPrefix) else { continue }
        let relative = String(file.path.dropFirst(rootPrefix.count))
        let content = try hashFile(file)
        let attrs = try FileManager.default.attributesOfItem(atPath: file.path)
        let mtime = UInt64((attrs[.modificationDate] as? Date)?.timeIntervalSince1970 ?? 0)
        sources[content.digest] = file
        do {
            entries.append(try ManifestEntry(path: relative, sourceID: file.path, sourceKind: "LocalDirectory",
                                             mode: 0o444, mtimeSecs: mtime, content: content,
                                             reason: "human-selected local directory"))
        } catch {
            FileHandle.standardError.write(Data("oxdirtest: skipping \(relative): \(error)\n".utf8))
        }
    }
    let bytes = entries.reduce(0) { $0 + $1.content.size }
    return Indexed(entries: entries, sources: sources, files: entries.count, bytes: bytes)
}

// MARK: - REPL helpers

func err(_ s: String) { FileHandle.standardError.write(Data((s + "\n").utf8)) }

/// Reject selections that escape the source root or use `..`.
func parseSelections(sourceRoot: URL, _ values: [String]) -> [String]? {
    var unique = Set<String>()
    for value in values {
        let parts = value.split(separator: "/", omittingEmptySubsequences: true).map(String.init)
        if value.hasPrefix("/") || parts.contains("..") { err("oxdirtest: selection must be a relative path inside the source: \(value)"); return nil }
        unique.insert(parts.joined(separator: "/"))
    }
    return unique.sorted()
}

func publishSelection(sourceRoot: URL, source: LocalDirectorySource, workspace: Workspace,
                      generation: inout UInt64, selected: Set<String>) -> Bool {
    err("oxdirtest: scanning \(selected.count) selected directories...")
    let indexed: Indexed
    do { indexed = try indexFiles(sourceRoot: sourceRoot, selections: selected.sorted()) }
    catch { err("oxdirtest: scan failed: \(error)"); return false }

    let next = generation &+ 1
    let previous = source.replace(indexed.sources)
    do {
        let outcome = try workspace.apply(Manifest(sessionID: "human-selection", generation: next, entries: indexed.entries))
        generation = next
        err("oxdirtest: generation=\(generation) directories=\(selected.count) files=\(indexed.files) bytes=\(indexed.bytes) available=\(outcome.available) stopped=\(outcome.stopped)")
        if outcome.stopped > 0 {
            err("oxdirtest: WARNING \(outcome.stopped) of \(indexed.files) file(s) did not fit the cache and are NOT visible")
        }
        return true
    } catch {
        _ = source.replace(previous)
        err("oxdirtest: selection rejected: \(error)")
        err("oxdirtest: selection unchanged; remove directories or raise --cache-bytes")
        return false
    }
}

func printMetrics(_ workspace: Workspace) {
    guard let t = try? workspace.cacheTelemetry() else { err("oxdirtest: metrics unavailable"); return }
    let (evicted, blocked) = workspace.evictionCounters()
    let (capacity, remaining) = workspace.cacheCapacity()
    err("level=INFO action=metrics "
        + "fetched_objects=\(t.fetchedObjects) fetched_bytes=\(t.fetchedBytes) "
        + "evicted_objects=\(evicted) evict_blocked=\(blocked) "
        + "catalog_transactions=\(t.catalogTransactions) directory_syncs=\(t.directorySyncs) "
        + "resident_objects=\(t.residentObjects) resident_bytes=\(t.residentBytes) pending_objects=\(t.pendingObjects) "
        + "cache_capacity=\(capacity) cache_remaining=\(remaining) "
        + "(nfs_* omitted: FSKit mount is Phase 7)")
}

func printStatus(_ workspace: Workspace, generation: UInt64, selected: Set<String>) {
    let snap = workspace.snapshot()
    let visible = snap.nodes.values.filter { $0.kind == .file && ($0.file.map { $0.synthetic == nil } ?? false) }.count
    err("oxdirtest: generation=\(generation) selections=\(selected.count) [\(selected.sorted().joined(separator: ", "))] visible_files=\(visible)")
}

// MARK: - main

func usage() {
    err("""
    usage: oxdirtest --source DIR [--state DIR] [--cache-bytes N]
      --source DIR       directory tree to select from (required)
      --state DIR        workspace state+cache dir (default: a temp dir)
      --cache-bytes N     cache capacity in bytes (default: 1073741824)
    """)
}

var sourceArg: String?
var stateArg: String?
var cacheBytes: UInt64 = DEFAULT_CACHE_MAX_BYTES
do {
    var args = Array(CommandLine.arguments.dropFirst())
    var i = 0
    while i < args.count {
        switch args[i] {
        case "--source": i += 1; sourceArg = i < args.count ? args[i] : nil
        case "--state": i += 1; stateArg = i < args.count ? args[i] : nil
        case "--cache-bytes": i += 1; cacheBytes = (i < args.count ? UInt64(args[i]) : nil) ?? cacheBytes
        case "--help", "-h": usage(); exit(0)
        default: err("oxdirtest: unknown argument \(args[i])"); usage(); exit(2)
        }
        i += 1
    }
}
guard let sourceArg else { usage(); exit(2) }
// Canonicalize via realpath so the root matches paths returned by directory
// enumeration (macOS /var -> /private/var); a plain resolvingSymlinksInPath
// leaves /var unresolved, and the relative-path strip would then drop files.
func realpathOf(_ path: String) -> String {
    var buffer = [CChar](repeating: 0, count: Int(PATH_MAX))
    return realpath(path, &buffer) != nil ? String(cString: buffer) : path
}
let sourceRoot = URL(fileURLWithPath: realpathOf(sourceArg))
guard (try? sourceRoot.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) == true else {
    err("oxdirtest: --source is not a directory: \(sourceArg)"); exit(2)
}
let stateRoot = URL(fileURLWithPath: stateArg ?? FileManager.default.temporaryDirectory.appendingPathComponent("oxdirtest-\(ProcessInfo.processInfo.processIdentifier)").path)

let source = LocalDirectorySource()
let workspace: Workspace
do {
    workspace = try Workspace.open(root: stateRoot, source: source, config: CacheConfig(maxBytes: cacheBytes))
} catch { err("oxdirtest: failed to open workspace: \(error)"); exit(1) }

err("oxdirtest: source=\(sourceRoot.path) state=\(stateRoot.path) cache_bytes=\(cacheBytes)")
err("oxdirtest: type 'help' for commands. Selection drives the real verified cache; the FSKit mount is Phase 7.")

var generation: UInt64 = 0
var selected = Set<String>()

while true {
    FileHandle.standardError.write(Data("oxdirtest> ".utf8))
    guard let line = readLine(strippingNewline: true) else { break }
    let words = line.split(whereSeparator: { $0 == " " || $0 == "\t" }).map(String.init)
    guard let command = words.first else { continue }
    switch command {
    case "select" where words.count > 1:
        guard let additions = parseSelections(sourceRoot: sourceRoot, Array(words.dropFirst())) else { continue }
        let candidate = selected.union(additions)
        if candidate == selected { err("oxdirtest: directories already selected; selection unchanged"); continue }
        if publishSelection(sourceRoot: sourceRoot, source: source, workspace: workspace, generation: &generation, selected: candidate) {
            selected = candidate
        }
    case "select":
        err("usage: select DIR [DIR ...]")
    case "clear":
        if publishSelection(sourceRoot: sourceRoot, source: source, workspace: workspace, generation: &generation, selected: []) {
            selected = []
        }
    case "status":
        printStatus(workspace, generation: generation, selected: selected)
    case "metrics":
        printMetrics(workspace)
    case "help":
        err("commands:\n  select DIR [DIR ...]  add backing directories to the selection\n  clear                 reset to no selected directories\n  status                show current selection + visible file count\n  metrics               show cumulative cache and durability metrics\n  quit                  exit")
    case "quit", "exit":
        exit(0)
    default:
        err("oxdirtest: unknown command '\(command)' (try 'help')")
    }
}
