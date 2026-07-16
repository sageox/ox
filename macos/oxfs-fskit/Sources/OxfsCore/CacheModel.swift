import Foundation

/// Pure in-memory reference model of the content cache — the differential
/// oracle. Port of `crates/cachesim/src/lib.rs::CacheModel`.
///
/// One spec, two mechanisms: this model realizes the cache's policy semantics
/// with plain in-memory maps; `DiskContentCache` realizes them with SQLite +
/// disk. A harness runs the same op stream through both and asserts the resident
/// key sets agree — the independence is what lets the diff catch policy bugs.
public final class CacheModel {
    private struct Obj { var size: UInt64; var accessEpoch: UInt64; var insertSeq: UInt64; var reference: Bool; var frequency: Int }

    private let capacity: UInt64
    private let order: EvictionOrder
    private var epoch: UInt64 = 0
    private var resident: [String: Obj] = [:]
    private var nextInsertSeq: UInt64 = 1
    private var clockHand: UInt64 = 0

    public init(capacity: UInt64, order: EvictionOrder) {
        self.capacity = capacity
        self.order = order
    }

    /// Apply one manifest's desired entries as `(key, size)` in manifest order.
    /// Mirrors `Workspace.apply` -> `materializeMissingBatch` for a workload
    /// whose source always has the content (no fetch failures / rollbacks).
    public func apply(_ entries: [(key: String, size: UInt64)]) {
        epoch += 1

        // Admission (AdmitEverything): first-occurrence dedup, residency-skip,
        // break at the first object larger than the whole cache.
        var seen = Set<String>()
        var missing: [(String, UInt64)] = []
        for (key, size) in entries {
            if !seen.insert(key).inserted { continue }
            if resident[key] != nil { continue }
            if size > capacity { break }
            missing.append((key, size))
        }

        var pending: [(key: String, size: UInt64, seq: UInt64)] = []
        var used = resident.values.reduce(0) { $0 + $1.size }
        for (key, size) in missing {
            if used + size > capacity {
                // Plan victims all-or-nothing.
                let residentBefore = resident
                let handBefore = clockHand
                let victims = planVictims(incoming: key, used: used, size: size)
                let freed = victims.reduce(0) { $0 + (resident[$1]?.size ?? 0) }
                if used + size - freed > capacity {
                    resident = residentBefore
                    clockHand = handBefore
                    break // StorageFull: evict nothing, keep what already fit
                }
                for v in victims { used -= resident[v]!.size; resident[v] = nil }
            }
            let seq = nextInsertSeq; nextInsertSeq += 1
            pending.append((key, size, seq))
            used += size
        }

        for p in pending {
            resident[p.key] = Obj(size: p.size, accessEpoch: epoch, insertSeq: p.seq, reference: false, frequency: 0)
        }
    }

    /// The ordered prefix of residents (excluding `incoming`) to evict to fit
    /// `size`, under the configured order. Mutates reference bits / clock hand
    /// exactly as the real catalog does (second-chance).
    private func planVictims(incoming: String, used: UInt64, size: UInt64) -> [String] {
        var cands = resident.filter { $0.key != incoming }.map { (key: $0.key, obj: $0.value) }
        switch order {
        case .leastRecentlySelectedGeneration:
            cands.sort { ($0.obj.accessEpoch, $0.key) < ($1.obj.accessEpoch, $1.key) }
        case .approxLeastFrequentlyUsed:
            cands.sort { ($0.obj.frequency, $0.obj.insertSeq, $0.key) < ($1.obj.frequency, $1.obj.insertSeq, $1.key) }
        case .clockSecondChance:
            cands.sort {
                let a = ($0.obj.insertSeq < clockHand ? 1 : 0, $0.obj.insertSeq, $0.key)
                let b = ($1.obj.insertSeq < clockHand ? 1 : 0, $1.obj.insertSeq, $1.key)
                return a < b
            }
            var cold: [(key: String, obj: Obj)] = [], hot: [(key: String, obj: Obj)] = []
            for c in cands {
                if c.obj.reference { resident[c.key]?.reference = false; hot.append(c) } else { cold.append(c) }
            }
            cands = cold + hot
        }

        var freed: UInt64 = 0
        var victims: [String] = []
        for c in cands {
            if used + size - freed <= capacity { break }
            freed += c.obj.size
            victims.append(c.key)
        }
        if order == .clockSecondChance, let last = victims.last, let obj = resident[last] {
            clockHand = obj.insertSeq &+ 1
        }
        return victims
    }

    public func touch(_ key: String) {
        guard resident[key] != nil else { return }
        switch order {
        case .leastRecentlySelectedGeneration: break
        case .clockSecondChance: resident[key]?.reference = true
        case .approxLeastFrequentlyUsed: resident[key]?.frequency += 1
        }
    }

    public func markMissing(_ key: String) { resident[key] = nil }

    public func residentKeys() -> [String] { resident.keys.sorted() }
    public func usedBytes() -> UInt64 { resident.values.reduce(0) { $0 + $1.size } }

    public func debugRows() -> [(key: String, seq: UInt64, ref: Bool, freq: Int)] {
        resident.map { (String($0.key.suffix(6)), $0.value.insertSeq, $0.value.reference, $0.value.frequency) }.sorted { $0.0 < $1.0 }
    }
    public func debugClockHand() -> UInt64 { clockHand }
}
