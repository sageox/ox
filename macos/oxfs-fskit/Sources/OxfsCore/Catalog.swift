import Foundation
import SQLite3

// Port of `crates/oxfs/src/cache_catalog.rs::Catalog` — the crash-safe SQLite
// catalog that is the source of truth for what is resident, with all three
// eviction policies. The Rust impl stores CLOCK/LFU policy state in an in-memory
// slot array; here we keep it in the `reference`/`frequency` columns directly,
// which is simpler and yields the same observable residency (what the
// differential oracle checks).

let SQLITE_TRANSIENT = unsafeBitCast(-1, to: sqlite3_destructor_type.self)

enum CatalogError: Error, Equatable {
    case sql(String)
    case reservationDisappeared
    case storageFull
}

enum Reservation { case resident, pending, refetch }

struct Victim: Equatable { let key: String; let size: UInt64; let access: UInt64 }
struct Gauges: Equatable { var residentObjects: UInt64 = 0; var residentBytes: UInt64 = 0; var pendingObjects: UInt64 = 0 }

private let STATE_PENDING: Int64 = 0
private let STATE_RESIDENT: Int64 = 1
private let STATE_EVICTED: Int64 = 2

final class Catalog {
    private var db: OpaquePointer?
    private let order: EvictionOrder
    private var inBatch = false
    private var epoch: UInt64 = 0
    private var usedBytes: UInt64 = 0
    private var nextInsertSeq: UInt64 = 1
    private var clockHand: UInt64 = 0
    private var batchUsedStart: UInt64?

    init(path: URL, order: EvictionOrder = .leastRecentlySelectedGeneration) throws {
        self.order = order
        guard sqlite3_open(path.path, &db) == SQLITE_OK else {
            throw CatalogError.sql("open: \(String(cString: sqlite3_errmsg(db)))")
        }
        sqlite3_busy_timeout(db, 5000)
        try exec("""
            PRAGMA journal_mode=WAL;
            PRAGMA synchronous=NORMAL;
            PRAGMA temp_store=MEMORY;
            CREATE TABLE IF NOT EXISTS cache_meta (
              singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
              epoch INTEGER NOT NULL,
              used_bytes INTEGER NOT NULL,
              next_insert_seq INTEGER NOT NULL DEFAULT 1,
              clock_hand INTEGER NOT NULL DEFAULT 0
            );
            INSERT OR IGNORE INTO cache_meta(singleton, epoch, used_bytes, next_insert_seq, clock_hand)
              VALUES (1, 0, 0, 1, 0);
            CREATE TABLE IF NOT EXISTS cache_objects (
              key TEXT PRIMARY KEY,
              size INTEGER NOT NULL CHECK (size >= 0),
              access_epoch INTEGER NOT NULL,
              state INTEGER NOT NULL CHECK (state IN (0, 1, 2)),
              insert_seq INTEGER NOT NULL DEFAULT 0,
              reference INTEGER NOT NULL DEFAULT 0,
              frequency INTEGER NOT NULL DEFAULT 0
            ) WITHOUT ROWID;
            CREATE INDEX IF NOT EXISTS cache_objects_evict ON cache_objects(state, access_epoch, key);
            CREATE INDEX IF NOT EXISTS cache_objects_clock ON cache_objects(state, insert_seq, key);
            """)
        let stmt = try Stmt(db, "SELECT epoch, used_bytes, next_insert_seq, clock_hand FROM cache_meta WHERE singleton=1")
        defer { stmt.finalize() }
        if stmt.step() == SQLITE_ROW {
            epoch = UInt64(stmt.int(0)); usedBytes = UInt64(stmt.int(1))
            nextInsertSeq = Swift.max(1, UInt64(stmt.int(2))); clockHand = UInt64(stmt.int(3))
        }
    }

    deinit { sqlite3_close(db) }

    // MARK: - batch lifecycle

    func beginBatch() throws {
        if inBatch { throw CatalogError.sql("batch already active") }
        try exec("BEGIN IMMEDIATE")
        inBatch = true
        batchUsedStart = usedBytes
        epoch = epoch &+ 1
        try run("UPDATE cache_meta SET epoch=?1 WHERE singleton=1", i64(epoch))
    }

    func commit() throws {
        guard inBatch else { return }
        try persistMeta()
        try exec("COMMIT")
        inBatch = false
        batchUsedStart = nil
    }

    func rollback() throws {
        if inBatch {
            try exec("ROLLBACK")
            inBatch = false
            usedBytes = batchUsedStart ?? usedBytes
            batchUsedStart = nil
            // Reload volatile scalars the rolled-back statements may have changed.
            let stmt = try Stmt(db, "SELECT next_insert_seq, clock_hand FROM cache_meta WHERE singleton=1")
            defer { stmt.finalize() }
            if stmt.step() == SQLITE_ROW { nextInsertSeq = Swift.max(1, UInt64(stmt.int(0))); clockHand = UInt64(stmt.int(1)) }
        }
    }

    // MARK: - residency

    func isResident(_ key: String, size: UInt64) throws -> Bool {
        let stmt = try Stmt(db, "SELECT 1 FROM cache_objects WHERE key=?1 AND size=?2 AND state=1")
        defer { stmt.finalize() }
        stmt.bindText(1, key); stmt.bindInt(2, i64(size))
        return stmt.step() == SQLITE_ROW
    }

    /// Reserve `key` for materialization, evicting per policy to make room.
    /// All-or-nothing: on `storageFull` no victims are consumed.
    func reserve(_ key: String, size: UInt64, capacity: UInt64) throws -> (Reservation, [Victim]) {
        let existing = try existingRow(key)
        if let e = existing, e.state == STATE_RESIDENT, e.size == i64(size) { return (.resident, []) }
        if let e = existing, e.state == STATE_PENDING, e.size == i64(size) { return (.pending, []) }

        var wasEvicted = existing?.state == STATE_EVICTED
        let clearExisting = existing.map { $0.state == STATE_PENDING || $0.state == STATE_RESIDENT } ?? false
        var plannedUsed = usedBytes
        var victims: [Victim] = []

        if let e = existing, e.state == STATE_PENDING || e.state == STATE_RESIDENT {
            let stored = UInt64(e.size)
            plannedUsed = plannedUsed >= stored ? plannedUsed - stored : 0
            if e.state == STATE_RESIDENT {
                wasEvicted = true
                victims.append(Victim(key: key, size: stored, access: UInt64(e.accessEpoch)))
            }
        }

        let target = capacity >= size ? capacity - size : 0
        var refClears: [String] = []
        var newClockHand: UInt64?
        if plannedUsed > target {
            let plan = try planVictims(incoming: key, used: plannedUsed, size: size, target: target)
            for v in plan.victims { plannedUsed = plannedUsed >= v.size ? plannedUsed - v.size : 0 }
            victims.append(contentsOf: plan.victims)
            refClears = plan.refClears
            newClockHand = plan.newClockHand
        }
        if plannedUsed > target { throw CatalogError.storageFull }

        // Commit the plan (only reached when it fits — all-or-nothing).
        if clearExisting { try run("UPDATE cache_objects SET state=2 WHERE key=?1", text: key) }
        for victim in victims where victim.key != key {
            try run("UPDATE cache_objects SET state=2 WHERE key=?1", text: victim.key)
        }
        for hot in refClears { try run("UPDATE cache_objects SET reference=0 WHERE key=?1", text: hot) }
        if let hand = newClockHand { clockHand = hand }
        usedBytes = plannedUsed

        let seq = nextInsertSeq; nextInsertSeq &+= 1
        try run("""
            INSERT INTO cache_objects(key,size,access_epoch,state,insert_seq,reference,frequency)
              VALUES(?1,?2,?3,0,?4,0,0)
            ON CONFLICT(key) DO UPDATE SET size=excluded.size, access_epoch=excluded.access_epoch,
              state=0, insert_seq=excluded.insert_seq, reference=0, frequency=0
            """, text: key, i64(size), i64(epoch), i64(seq))
        usedBytes = usedBytes &+ size
        try persistMetaIfAutocommit()
        return (wasEvicted ? .refetch : .pending, victims)
    }

    private struct VictimPlan { let victims: [Victim]; let refClears: [String]; let newClockHand: UInt64? }

    /// Compute the ordered victim prefix (excluding `incoming`) needed to fit,
    /// under the configured policy. Pure — performs no writes.
    private func planVictims(incoming: String, used: UInt64, size: UInt64, target: UInt64) throws -> VictimPlan {
        struct Cand { let key: String; let size: UInt64; let access: UInt64; let insertSeq: UInt64; let reference: Bool; let frequency: Int64 }
        var cands: [Cand] = []
        let stmt = try Stmt(db, "SELECT key,size,access_epoch,insert_seq,reference,frequency FROM cache_objects WHERE state=1 AND key<>?1")
        stmt.bindText(1, incoming)
        while stmt.step() == SQLITE_ROW {
            cands.append(Cand(key: stmt.text(0), size: UInt64(stmt.int(1)), access: UInt64(stmt.int(2)),
                              insertSeq: UInt64(stmt.int(3)), reference: stmt.int(4) != 0, frequency: stmt.int(5)))
        }
        stmt.finalize()

        var refClears: [String] = []
        switch order {
        case .leastRecentlySelectedGeneration:
            cands.sort { ($0.access, $0.key) < ($1.access, $1.key) }
        case .approxLeastFrequentlyUsed:
            cands.sort { ($0.frequency, $0.insertSeq, $0.key) < ($1.frequency, $1.insertSeq, $1.key) }
        case .clockSecondChance:
            // Sweep from the hand: objects ahead of the hand first.
            cands.sort {
                let a = ($0.insertSeq < clockHand ? 1 : 0, $0.insertSeq, $0.key)
                let b = ($1.insertSeq < clockHand ? 1 : 0, $1.insertSeq, $1.key)
                return a < b
            }
            var cold: [Cand] = [], hot: [Cand] = []
            for c in cands {
                if c.reference { refClears.append(c.key); hot.append(c) } else { cold.append(c) }
            }
            cands = cold + hot
        }

        var freed: UInt64 = 0
        var victims: [Victim] = []
        for c in cands {
            if used - freed <= target { break }
            freed += c.size
            victims.append(Victim(key: c.key, size: c.size, access: c.access))
        }
        var newClockHand: UInt64?
        if order == .clockSecondChance, let last = victims.last {
            newClockHand = (cands.first { $0.key == last.key }?.insertSeq ?? 0) &+ 1
        }
        return VictimPlan(victims: victims, refClears: refClears, newClockHand: newClockHand)
    }

    func finish(_ key: String, size: UInt64) throws {
        let changed = try run("UPDATE cache_objects SET size=?2, state=1, access_epoch=?3 WHERE key=?1 AND state=0",
                              text: key, i64(size), i64(epoch))
        if changed != 1 { throw CatalogError.reservationDisappeared }
    }

    func release(_ key: String) throws { try dropToEvicted(key, states: [STATE_PENDING]) }
    func markMissing(_ key: String) throws { try dropToEvicted(key, states: [STATE_PENDING, STATE_RESIDENT]) }

    private func dropToEvicted(_ key: String, states: [Int64]) throws {
        let inList = states.map(String.init).joined(separator: ",")
        let stmt = try Stmt(db, "SELECT size FROM cache_objects WHERE key=?1 AND state IN (\(inList))")
        stmt.bindText(1, key)
        let size: UInt64? = stmt.step() == SQLITE_ROW ? UInt64(stmt.int(0)) : nil
        stmt.finalize()
        try run("UPDATE cache_objects SET state=2 WHERE key=?1", text: key)
        if let size { usedBytes = usedBytes >= size ? usedBytes - size : 0; try persistMetaIfAutocommit() }
    }

    func importResident(_ key: String, size: UInt64, access: UInt64) throws {
        let check = try Stmt(db, "SELECT 1 FROM cache_objects WHERE key=?1")
        check.bindText(1, key)
        let exists = check.step() == SQLITE_ROW
        check.finalize()
        if exists { return }
        let seq = nextInsertSeq; nextInsertSeq &+= 1
        let inserted = try run("INSERT OR IGNORE INTO cache_objects(key,size,access_epoch,state,insert_seq,reference,frequency) VALUES(?1,?2,?3,1,?4,0,0)",
                               text: key, i64(size), i64(access), i64(seq))
        if inserted == 1 { usedBytes = usedBytes &+ size }
        epoch = Swift.max(epoch, access)
        try run("UPDATE cache_meta SET epoch=?1, used_bytes=?2, next_insert_seq=?3 WHERE singleton=1",
                i64(epoch), i64(usedBytes), i64(nextInsertSeq))
    }

    func touch(_ key: String) throws {
        switch order {
        case .leastRecentlySelectedGeneration: return
        case .clockSecondChance: try run("UPDATE cache_objects SET reference=1 WHERE key=?1 AND state=1", text: key)
        case .approxLeastFrequentlyUsed: try run("UPDATE cache_objects SET frequency=frequency+1 WHERE key=?1 AND state=1", text: key)
        }
    }

    func debugRows() throws -> [(key: String, seq: UInt64, ref: Bool, freq: Int)] {
        let stmt = try Stmt(db, "SELECT key,insert_seq,reference,frequency FROM cache_objects WHERE state=1")
        defer { stmt.finalize() }
        var out: [(String, UInt64, Bool, Int)] = []
        while stmt.step() == SQLITE_ROW { out.append((String(stmt.text(0).suffix(6)), UInt64(stmt.int(1)), stmt.int(2) != 0, Int(stmt.int(3)))) }
        return out.sorted { $0.0 < $1.0 }
    }
    func debugClockHand() -> UInt64 { clockHand }

    func pendingKeys() throws -> [String] { try keys("SELECT key FROM cache_objects WHERE state=0") }
    func residentKeys() throws -> [String] { try keys("SELECT key FROM cache_objects WHERE state=1 ORDER BY key") }

    func clearPending() throws {
        let stmt = try Stmt(db, "SELECT COALESCE(SUM(size),0) FROM cache_objects WHERE state=0")
        let pendingBytes: UInt64 = stmt.step() == SQLITE_ROW ? UInt64(stmt.int(0)) : 0
        stmt.finalize()
        try exec("UPDATE cache_objects SET state=2 WHERE state=0")
        usedBytes = usedBytes >= pendingBytes ? usedBytes - pendingBytes : 0
        try persistMetaIfAutocommit()
    }

    func gauges() throws -> Gauges {
        let stmt = try Stmt(db, """
            SELECT COALESCE(SUM(CASE WHEN state=1 THEN 1 ELSE 0 END),0),
                   COALESCE(SUM(CASE WHEN state=1 THEN size ELSE 0 END),0),
                   COALESCE(SUM(CASE WHEN state=0 THEN 1 ELSE 0 END),0)
            FROM cache_objects
            """)
        defer { stmt.finalize() }
        guard stmt.step() == SQLITE_ROW else { return Gauges() }
        return Gauges(residentObjects: UInt64(stmt.int(0)), residentBytes: UInt64(stmt.int(1)), pendingObjects: UInt64(stmt.int(2)))
    }

    // MARK: - helpers

    private struct Row { let state: Int64; let size: Int64; let accessEpoch: Int64 }

    private func existingRow(_ key: String) throws -> Row? {
        let stmt = try Stmt(db, "SELECT state, size, access_epoch FROM cache_objects WHERE key=?1")
        defer { stmt.finalize() }
        stmt.bindText(1, key)
        guard stmt.step() == SQLITE_ROW else { return nil }
        return Row(state: stmt.int(0), size: stmt.int(1), accessEpoch: stmt.int(2))
    }

    private func keys(_ sql: String) throws -> [String] {
        let stmt = try Stmt(db, sql); defer { stmt.finalize() }
        var out: [String] = []
        while stmt.step() == SQLITE_ROW { out.append(stmt.text(0)) }
        return out
    }

    private func persistMetaIfAutocommit() throws { if !inBatch { try persistMeta() } }
    private func persistMeta() throws {
        try run("UPDATE cache_meta SET used_bytes=?1, next_insert_seq=?2, clock_hand=?3 WHERE singleton=1",
                i64(usedBytes), i64(nextInsertSeq), i64(clockHand))
    }

    private func i64(_ v: UInt64) -> Int64 { Int64(bitPattern: v) }

    private func exec(_ sql: String) throws {
        var err: UnsafeMutablePointer<CChar>?
        if sqlite3_exec(db, sql, nil, nil, &err) != SQLITE_OK {
            let m = err.map { String(cString: $0) } ?? "unknown"
            sqlite3_free(err)
            throw CatalogError.sql(m)
        }
    }

    @discardableResult
    private func run(_ sql: String, text: String? = nil, _ ints: Int64...) throws -> Int32 {
        let stmt = try Stmt(db, sql); defer { stmt.finalize() }
        var index: Int32 = 1
        if let text { stmt.bindText(index, text); index += 1 }
        for value in ints { stmt.bindInt(index, value); index += 1 }
        let rc = stmt.step()
        guard rc == SQLITE_DONE || rc == SQLITE_ROW else {
            throw CatalogError.sql("run: \(String(cString: sqlite3_errmsg(db)))")
        }
        return sqlite3_changes(db)
    }
}

/// Thin RAII wrapper over a prepared statement.
final class Stmt {
    let handle: OpaquePointer?
    init(_ db: OpaquePointer?, _ sql: String) throws {
        var h: OpaquePointer?
        guard sqlite3_prepare_v2(db, sql, -1, &h, nil) == SQLITE_OK else {
            throw CatalogError.sql("prepare: \(String(cString: sqlite3_errmsg(db)))")
        }
        handle = h
    }
    func bindText(_ i: Int32, _ s: String) { sqlite3_bind_text(handle, i, s, -1, SQLITE_TRANSIENT) }
    func bindInt(_ i: Int32, _ v: Int64) { sqlite3_bind_int64(handle, i, v) }
    func step() -> Int32 { sqlite3_step(handle) }
    func int(_ col: Int32) -> Int64 { sqlite3_column_int64(handle, col) }
    func text(_ col: Int32) -> String {
        guard let c = sqlite3_column_text(handle, col) else { return "" }
        return String(cString: c)
    }
    func finalize() { sqlite3_finalize(handle) }
}
