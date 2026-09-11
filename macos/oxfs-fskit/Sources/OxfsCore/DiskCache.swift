import Foundation
import CryptoKit

/// Port of `crates/oxfs/src/cache.rs::ContentCache` — the disk-backed, verified,
/// crash-safe content cache. Objects live under `objects/<tenant-hash>/<key>`;
/// materialization streams through a verifying writer (size + SHA-256), fsyncs,
/// and atomically renames into place. A batch is one SQLite transaction guarded
/// by an fsynced `active-apply.v1` journal so a crash leaves the catalog and the
/// object tree consistent.
public final class DiskContentCache: ContentCache, @unchecked Sendable {
    private let root: URL
    private let source: ContentSource
    private let config: CacheConfig
    private let catalog: Catalog
    private let instanceLockFD: Int32
    private let lock = NSLock()

    private var inBatch = false
    private var batchDirectories: Set<String> = []
    private var batchVictims: [Victim] = []
    private var validated: Set<String> = []
    private var tempNonce: UInt64 = 0
    private var failNextCommitFlag = false
    private var openPins: [String: Int] = [:]

    // telemetry counters
    private var fetchedObjects: UInt64 = 0
    private var fetchedBytes: UInt64 = 0
    private var directorySyncs: UInt64 = 0
    private var catalogTransactions: UInt64 = 0
    private var evicted: UInt64 = 0
    private var blocked: UInt64 = 0

    public init(root: URL, source: ContentSource, config: CacheConfig = .default) throws {
        if config.maxBytes == 0 { throw FetchError.io("cache max_bytes must be positive") }
        self.root = root
        self.source = source
        self.config = config
        try FileManager.default.createDirectory(at: root.appendingPathComponent("objects"), withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: root.appendingPathComponent("tmp"), withIntermediateDirectories: true)
        let lockPath = root.appendingPathComponent("cache.lock").path
        let lockFD = open(lockPath, O_CREAT | O_RDWR, S_IRUSR | S_IWUSR)
        guard lockFD >= 0 else {
            throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
        }
        guard lockf(lockFD, F_TLOCK, 0) == 0 else {
            let code = errno
            close(lockFD)
            throw POSIXError(POSIXErrorCode(rawValue: code) ?? .EBUSY)
        }
        var retainLock = false
        defer {
            if !retainLock {
                _ = lockf(lockFD, F_ULOCK, 0)
                close(lockFD)
            }
        }
        self.instanceLockFD = lockFD
        self.catalog = try Catalog(path: root.appendingPathComponent("catalog.sqlite"), order: config.eviction)
        try removeOrphanTemps()
        try migrateMetadataV1()
        try recoverPending()
        retainLock = true
    }

    deinit {
        _ = lockf(instanceLockFD, F_ULOCK, 0)
        close(instanceLockFD)
    }

    // MARK: - keys / paths

    public func storageKey(_ reference: ContentRef) -> String {
        let tenantHash = SHA256.hash(data: Data(reference.tenant.utf8))
        return "\(ContentRef.hexDigest(tenantHash))/\(reference.storageKey)"
    }
    private func objectPath(_ reference: ContentRef) -> URL { keyPath(storageKey(reference)) }
    private func keyPath(_ key: String) -> URL { root.appendingPathComponent("objects").appendingPathComponent(key) }

    // MARK: - simple accessors

    public func capacity() -> UInt64 { config.maxBytes }
    public func used() -> UInt64 { lock.lock(); defer { lock.unlock() }; return (try? catalog.gauges().residentBytes) ?? config.maxBytes }
    public func remaining() -> UInt64 { config.maxBytes - Swift.min(config.maxBytes, used()) }
    public func evictionCounters() -> (evicted: UInt64, blocked: UInt64) { lock.lock(); defer { lock.unlock() }; return (evicted, blocked) }
    public func residentKeys() throws -> [String] { lock.lock(); defer { lock.unlock() }; return try catalog.residentKeys() }

    public func telemetry() throws -> CacheTelemetrySnapshot {
        lock.lock(); defer { lock.unlock() }
        let g = try catalog.gauges()
        return CacheTelemetrySnapshot(
            fetchedObjects: fetchedObjects, fetchedBytes: fetchedBytes,
            residentObjects: g.residentObjects, residentBytes: g.residentBytes,
            pendingObjects: g.pendingObjects, directorySyncs: directorySyncs,
            catalogTransactions: catalogTransactions
        )
    }

    /// Test hook (parity with `fail_next_commit`).
    public func failNextCommit() { lock.lock(); defer { lock.unlock() }; failNextCommitFlag = true }

    public func debugRows() throws -> [(key: String, seq: UInt64, ref: Bool, freq: Int)] {
        lock.lock(); defer { lock.unlock() }; return try catalog.debugRows()
    }
    public func debugClockHand() -> UInt64 { lock.lock(); defer { lock.unlock() }; return catalog.debugClockHand() }

    // MARK: - residency (with same-size corruption healing)

    public func resident(_ reference: ContentRef) throws -> ResidentContent? {
        lock.lock(); defer { lock.unlock() }
        return try residentLocked(reference)
    }

    private func residentLocked(_ reference: ContentRef) throws -> ResidentContent? {
        let key = storageKey(reference)
        guard try catalog.isResident(key, size: reference.size) else { return nil }
        let path = objectPath(reference)
        let fm = FileManager.default
        var isDir: ObjCBool = false
        guard fm.fileExists(atPath: path.path, isDirectory: &isDir), !isDir.boolValue else {
            try catalog.markMissing(key); return nil
        }
        let len = (try? fm.attributesOfItem(atPath: path.path))?[.size] as? UInt64
        guard len == reference.size else {
            try? fm.removeItem(at: path); try catalog.markMissing(key); return nil
        }
        if !validated.contains(key) {
            let digestMatches = try reference.algorithm == "sha256"
                && hashFile(path) == reference.digest.lowercased()
            if !digestMatches {
                try? fm.removeItem(at: path); try catalog.markMissing(key); return nil
            }
            validated.insert(key)
        }
        return ResidentContent(storageKey: key, size: reference.size)
    }

    // MARK: - batch lifecycle

    public func beginBatch(_ admissions: [ContentRef]) throws {
        lock.lock(); defer { lock.unlock() }
        let journal = root.appendingPathComponent("active-apply.v1")
        let lines = admissions.map { storageKey($0) }.joined(separator: "\n")
        try Data((lines.isEmpty ? "" : lines + "\n").utf8).write(to: journal, options: .atomic)
        try fsyncFile(journal)
        try syncDirectory(root)
        batchDirectories = []
        batchVictims = []
        inBatch = true
        do { try catalog.beginBatch() }
        catch { inBatch = false; try? FileManager.default.removeItem(at: journal); throw error }
        catalogTransactions &+= 1
    }

    public func commitBatch() throws {
        lock.lock(); defer { lock.unlock() }
        for dir in batchDirectories { try syncDirectory(URL(fileURLWithPath: dir)) }
        directorySyncs &+= UInt64(batchDirectories.count)
        if failNextCommitFlag { failNextCommitFlag = false; throw FetchError.io("injected cache commit failure") }
        let journal = root.appendingPathComponent("active-apply.v1")
        if !batchVictims.isEmpty {
            let existing = (try? String(contentsOf: journal, encoding: .utf8)) ?? ""
            let appended = existing + batchVictims.map { $0.key }.joined(separator: "\n") + "\n"
            try Data(appended.utf8).write(to: journal, options: .atomic)
            try fsyncFile(journal)
        }
        try catalog.commit()
        for victim in batchVictims { try evictVictim(victim) }
        batchDirectories = []
        batchVictims = []
        inBatch = false
        removeIfExists(journal)
    }

    public func rollbackBatch() throws {
        lock.lock(); defer { lock.unlock() }
        batchVictims = [] // dropped WITHOUT unlink — rollback restores them resident
        try catalog.rollback()
        try recoverApplyJournalLocked()
        batchDirectories = []
        inBatch = false
    }

    // MARK: - materialization

    public func materialize(_ reference: ContentRef) throws -> ResidentContent {
        if let rc = try resident(reference) { return rc }
        return try materializeMissing(reference)
    }

    public func materializeMissing(_ reference: ContentRef) throws -> ResidentContent {
        lock.lock(); defer { lock.unlock() }
        // A concurrent caller may have completed while this caller waited for
        // the cache lock. Recheck before reserving: reserving an already
        // resident row is intentionally a no-op, but fetching it again is not.
        if let resident = try residentLocked(reference) {
            return resident
        }
        let key = storageKey(reference)
        _ = try cacheReserveLocked(key, reference.size) // may throw StorageFull
        do {
            return try fetchReservedLocked(reference, key)
        } catch {
            try? catalog.release(key)
            throw error
        }
    }

    public func materializeMissingBatch(_ references: [ContentRef]) throws -> [String] {
        lock.lock(); defer { lock.unlock() }
        // Phase 1: reserve ALL in order (each stays state=0 pending — not an
        // eviction candidate for its batch-mates), stopping at StorageFull. This
        // matches the Rust batch, where reservations precede any fetch/finish, so
        // an object admitted earlier in the batch is never evicted by a later one.
        var reserved: [(ref: ContentRef, key: String)] = []
        for reference in references {
            let key = storageKey(reference)
            do { _ = try cacheReserveLocked(key, reference.size) }
            catch FetchError.io(let m) where m == "StorageFull" { break }
            reserved.append((reference, key))
        }
        // Phase 2: fetch + verify + finish each reserved object.
        var out: [String] = []
        for (reference, key) in reserved {
            do { out.append(try fetchReservedLocked(reference, key).storageKey) }
            catch { try? catalog.release(key); throw error }
        }
        return out
    }

    public func openContent(_ reference: ContentRef) throws -> OpenContent {
        lock.lock(); defer { lock.unlock() }
        guard try residentLocked(reference) != nil else { throw FetchError.notFound }
        let path = objectPath(reference)
        // Residency checks may use the per-process validation cache, but every
        // data open re-verifies the object. This prevents same-size mutation
        // after an earlier lookup from ever reaching a filesystem reader.
        let handle = try FileHandle(forReadingFrom: path)
        let digest: String
        do { digest = try hashHandle(handle) }
        catch { try? handle.close(); throw error }
        guard digest == reference.digest.lowercased() else {
            try? handle.close()
            try removeExisting(path)
            try catalog.markMissing(storageKey(reference))
            validated.remove(storageKey(reference))
            throw FetchError.wrongDigest(expected: reference.digest, actual: "on-disk corruption")
        }
        try handle.seek(toOffset: 0)
        try catalog.touch(storageKey(reference))
        let key = storageKey(reference)
        openPins[key, default: 0] += 1
        return DiskOpenContent(handle: handle, size: reference.size) { [weak self] in
            guard let self else { return }
            self.lock.lock(); defer { self.lock.unlock() }
            let count = self.openPins[key, default: 0]
            if count <= 1 { self.openPins[key] = nil }
            else { self.openPins[key] = count - 1 }
        }
    }

    // MARK: - internals

    private func cacheReserveLocked(_ key: String, _ size: UInt64) throws -> Bool {
        if size > config.maxBytes { throw FetchError.io("StorageFull") }
        let reservation: Reservation
        let victims: [Victim]
        do {
            (reservation, victims) = try catalog.reserve(
                key, size: size, capacity: config.maxBytes,
                excluding: Set(openPins.keys)
            )
        } catch CatalogError.storageFull {
            blocked &+= 1
            throw FetchError.io("StorageFull")
        }
        if inBatch {
            batchVictims.append(contentsOf: victims)
        } else {
            for victim in victims { try evictVictim(victim) }
        }
        return reservation == .refetch
    }

    private func fetchReservedLocked(_ reference: ContentRef, _ key: String) throws -> ResidentContent {
        tempNonce &+= 1
        let temp = root.appendingPathComponent("tmp").appendingPathComponent("\(ProcessInfo.processInfo.processIdentifier)-\(tempNonce).part")
        let final = objectPath(reference)
        try FileManager.default.createDirectory(at: final.deletingLastPathComponent(), withIntermediateDirectories: true)
        FileManager.default.createFile(atPath: temp.path, contents: nil)
        let handle = try FileHandle(forWritingTo: temp)
        let writer = VerifyingFileWriter(handle: handle, limit: reference.size)
        do {
            try source.fetch(reference, into: writer)
        } catch {
            try? handle.close(); try? FileManager.default.removeItem(at: temp); throw error
        }
        let (size, digest) = writer.finish()
        try handle.synchronize()
        try handle.close()

        if size != reference.size {
            try? FileManager.default.removeItem(at: temp)
            throw FetchError.wrongSize(expected: reference.size, actual: size)
        }
        if reference.algorithm != "sha256" || digest.lowercased() != reference.digest.lowercased() {
            try? FileManager.default.removeItem(at: temp)
            throw FetchError.wrongDigest(expected: reference.digest, actual: digest)
        }
        // Atomic publish: rename temp → final, then fsync (or defer) the directory.
        removeIfExists(final)
        try FileManager.default.moveItem(at: temp, to: final)
        try syncOrDeferDirectory(final.deletingLastPathComponent())
        try catalog.finish(key, size: size)
        validated.insert(key)
        fetchedObjects &+= 1
        fetchedBytes &+= size
        return ResidentContent(storageKey: key, size: size)
    }

    private func syncOrDeferDirectory(_ directory: URL) throws {
        if inBatch {
            batchDirectories.insert(directory.path)
        } else {
            try syncDirectory(directory)
            directorySyncs &+= 1
        }
    }

    private func evictVictim(_ victim: Victim) throws {
        try removeExisting(keyPath(victim.key))
        validated.remove(victim.key)
        evicted &+= 1
    }

    // MARK: - recovery

    private func removeOrphanTemps() throws {
        let tmp = root.appendingPathComponent("tmp")
        for entry in (try? FileManager.default.contentsOfDirectory(at: tmp, includingPropertiesForKeys: nil)) ?? [] {
            try? FileManager.default.removeItem(at: entry)
        }
    }

    private func migrateMetadataV1() throws {
        let path = root.appendingPathComponent("metadata.v1")
        guard let text = try? String(contentsOf: path, encoding: .utf8) else { return }
        for line in text.split(separator: "\n", omittingEmptySubsequences: true) {
            let fields = line.split(separator: "\t", omittingEmptySubsequences: false)
            guard fields.count == 3, let size = UInt64(fields[1]), let access = UInt64(fields[2]) else { continue }
            let key = String(fields[0])
            let object = keyPath(key)
            if let len = try? FileManager.default.attributesOfItem(atPath: object.path)[.size] as? UInt64, len == size {
                try catalog.importResident(key, size: size, access: access)
            }
        }
        removeIfExists(path)
    }

    private func recoverPending() throws {
        try recoverApplyJournalLocked()
        for key in try catalog.pendingKeys() { removeIfExists(keyPath(key)) }
        try catalog.clearPending()
    }

    private func recoverApplyJournalLocked() throws {
        let path = root.appendingPathComponent("active-apply.v1")
        guard let text = try? String(contentsOf: path, encoding: .utf8) else { return }
        for line in text.split(separator: "\n", omittingEmptySubsequences: true) {
            let key = String(line)
            let len = ((try? FileManager.default.attributesOfItem(atPath: keyPath(key).path))?[.size] as? UInt64) ?? 0
            if !(try catalog.isResident(key, size: len)) {
                removeIfExists(keyPath(key))
            }
        }
        removeIfExists(path)
    }

    // MARK: - fs helpers

    private func hashFile(_ url: URL) throws -> String {
        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }
        return try hashHandle(handle)
    }

    private func hashHandle(_ handle: FileHandle) throws -> String {
        var hasher = SHA256()
        while true {
            let chunk = try handle.read(upToCount: 65536) ?? Data()
            if chunk.isEmpty { break }
            hasher.update(data: chunk)
        }
        return ContentRef.hexDigest(hasher.finalize())
    }

    private func fsyncFile(_ url: URL) throws {
        let handle = try FileHandle(forWritingTo: url)
        do {
            try handle.synchronize()
            try handle.close()
        } catch {
            try? handle.close()
            throw error
        }
    }

    private func syncDirectory(_ url: URL) throws {
        let fd = open(url.path, O_RDONLY)
        guard fd >= 0 else {
            throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
        }
        defer { close(fd) }
        guard fsync(fd) == 0 else {
            throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
        }
    }

    private func removeIfExists(_ url: URL) { try? FileManager.default.removeItem(at: url) }

    private func removeExisting(_ url: URL) throws {
        if unlink(url.path) == 0 || errno == ENOENT { return }
        throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
    }
}

private final class DiskOpenContent: OpenContent {
    private let handle: FileHandle
    let size: UInt64
    private let onClose: () -> Void
    private let lock = NSLock()
    private var closed = false

    init(handle: FileHandle, size: UInt64, onClose: @escaping () -> Void) {
        self.handle = handle
        self.size = size
        self.onClose = onClose
    }
    func read(offset: UInt64, count: Int) throws -> [UInt8] {
        lock.lock(); defer { lock.unlock() }
        guard !closed else { throw FetchError.io("read after close") }
        guard offset < size, count > 0 else { return [] }
        try handle.seek(toOffset: offset)
        let bounded = min(UInt64(count), size - offset)
        return [UInt8](try handle.read(upToCount: Int(bounded)) ?? Data())
    }
    func close() throws {
        lock.lock()
        if closed { lock.unlock(); return }
        closed = true
        lock.unlock()
        defer { onClose() }
        try handle.close()
    }
    deinit { try? close() }
}

/// A `ContentWriter` that streams to a file while hashing (SHA-256), counting,
/// and enforcing the declared size limit — port of Rust `VerifyingWriter`.
final class VerifyingFileWriter: ContentWriter {
    private let handle: FileHandle
    private var hasher = SHA256()
    private var size: UInt64 = 0
    private let limit: UInt64

    init(handle: FileHandle, limit: UInt64) { self.handle = handle; self.limit = limit }

    func write(_ bytes: [UInt8]) throws {
        let remaining = limit >= size ? limit - size : 0
        if UInt64(bytes.count) > remaining { throw FetchError.io("source exceeded declared size") }
        let data = Data(bytes)
        try handle.write(contentsOf: data)
        hasher.update(data: data)
        size &+= UInt64(bytes.count)
    }

    func finish() -> (size: UInt64, digest: String) { (size, ContentRef.hexDigest(hasher.finalize())) }
}
