import Testing
import Foundation
@testable import OxfsCore

/// Differential oracle (Phase 4): run the same op stream through the real
/// `DiskContentCache` and the pure `CacheModel`, asserting resident-key parity
/// after every apply — for all three eviction policies. Port of the cachesim
/// differential harness. Independent realizations catch policy bugs, not just
/// plumbing.
@Suite struct OracleTests {
    final class MemorySource: ContentSource, @unchecked Sendable {
        let objects: [String: [UInt8]]
        init(_ objects: [String: [UInt8]]) { self.objects = objects }
        func fetch(_ reference: ContentRef, into output: ContentWriter) throws {
            guard let bytes = objects[reference.digest] else { throw FetchError.notFound }
            try output.write(bytes)
        }
    }
    struct Obj { let ref: ContentRef; let bytes: [UInt8] }

    private func tempRoot() -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("oxfs-oracle-\(UUID().uuidString)")
    }

    /// `n` distinct objects each exactly `size` bytes.
    private func makeObjects(_ n: Int, size: Int) -> ([Obj], MemorySource) {
        var map: [String: [UInt8]] = [:]
        var objs: [Obj] = []
        for i in 0..<n {
            var b = Array("object-\(i)-".utf8)
            while b.count < size { b.append(UInt8(i & 0xff)) }
            b = Array(b.prefix(size))
            let ref = ContentRef.forSha256(tenant: "t", bytes: b)
            map[ref.digest] = b
            objs.append(Obj(ref: ref, bytes: b))
        }
        return (objs, MemorySource(map))
    }

    private func driveRound(_ cache: DiskContentCache, _ model: CacheModel, _ subset: [Obj]) throws {
        let residentSet = Set(try cache.residentKeys())
        let candidates = subset.map { (key: cache.storageKey($0.ref), content: $0.ref, size: $0.ref.size) }
        let missing = AdmitEverything.select(capacity: cache.capacity(), resident: residentSet, candidates: candidates)
        try cache.beginBatch(subset.map { $0.ref })
        _ = try cache.materializeMissingBatch(missing)
        try cache.commitBatch()
        model.apply(subset.map { (cache.storageKey($0.ref), $0.ref.size) })
        let cacheKeys = Set(try cache.residentKeys())
        let modelKeys = Set(model.residentKeys())
        let diagnostic = cacheKeys == modelKeys ? "" :
            "residency diverged — cache=\(try cache.debugRows()) hand=\(cache.debugClockHand()) vs model=\(model.debugRows()) hand=\(model.debugClockHand())"
        #expect(cacheKeys == modelKeys, "\(diagnostic)")
    }

    private func touch(_ cache: DiskContentCache, _ model: CacheModel, _ obj: Obj) throws {
        _ = try cache.openBytes(obj.ref)
        model.touch(cache.storageKey(obj.ref))
    }

    /// Scripted, touch-sensitive workload that forces eviction; run against each
    /// policy and assert the real cache matches the model after every round.
    private func runWorkload(order: EvictionOrder) throws -> [String] {
        let root = tempRoot()
        defer { try? FileManager.default.removeItem(at: root) }
        let (o, source) = makeObjects(6, size: 10)
        let cache = try DiskContentCache(root: root, source: source, config: CacheConfig(maxBytes: 30, eviction: order))
        let model = CacheModel(capacity: 30, order: order)

        try driveRound(cache, model, [o[0], o[1], o[2]]) // fills cache (3×10 = 30)
        try touch(cache, model, o[0])                    // ref-bit / frequency on o0
        try driveRound(cache, model, [o[3]])             // must evict one — policies differ here
        try driveRound(cache, model, [o[0], o[1], o[2], o[3]])
        try touch(cache, model, o[3]); try touch(cache, model, o[3])
        try driveRound(cache, model, [o[4]])
        try driveRound(cache, model, [o[5]])
        try driveRound(cache, model, [o[0], o[1], o[2], o[3], o[4], o[5]])
        return try cache.residentKeys().sorted()
    }

    @Test func lruMatchesModel() throws { _ = try runWorkload(order: .leastRecentlySelectedGeneration) }
    @Test func clockMatchesModel() throws { _ = try runWorkload(order: .clockSecondChance) }
    @Test func lfuMatchesModel() throws { _ = try runWorkload(order: .approxLeastFrequentlyUsed) }

    /// Meta-check: the three policies are genuinely distinct — at least one pair
    /// reaches a different residency on this touch-sensitive workload. Guards
    /// against "every policy is secretly LRU".
    @Test func policiesAreDistinct() throws {
        let lru = try runWorkload(order: .leastRecentlySelectedGeneration)
        let clock = try runWorkload(order: .clockSecondChance)
        let lfu = try runWorkload(order: .approxLeastFrequentlyUsed)
        #expect(!(lru == clock && clock == lfu), "policies produced identical residency — not actually distinct")
    }
}
