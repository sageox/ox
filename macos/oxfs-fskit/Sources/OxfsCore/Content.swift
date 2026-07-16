import Foundation
import CryptoKit

/// Immutable, tenant-scoped identity for a complete object.
///
/// Port of `crates/oxfs/src/content.rs::ContentRef`. The digest is SHA-256 for
/// the LFS adapter; the algorithm is kept explicit so a future BLAKE3/Lore
/// adapter does not confuse the two namespaces.
public struct ContentRef: Hashable, Comparable, Sendable, Codable {
    public let tenant: String
    public let algorithm: String
    public let digest: String
    public let size: UInt64

    /// Validating constructor — mirrors Rust `ContentRef::new`.
    public init(tenant: String, algorithm: String, digest: String, size: UInt64) throws {
        if tenant.isEmpty || algorithm.isEmpty || digest.isEmpty {
            throw FetchError.invalidReference
        }
        if !algorithm.utf8.allSatisfy({ $0.isAsciiAlphanumeric || $0 == UInt8(ascii: "-") || $0 == UInt8(ascii: "_") }) {
            throw FetchError.invalidReference
        }
        if !digest.utf8.allSatisfy({ $0.isAsciiHexDigit }) {
            throw FetchError.invalidReference
        }
        self.tenant = tenant
        self.algorithm = algorithm
        self.digest = digest
        self.size = size
    }

    /// Unchecked internal initializer for values we construct ourselves
    /// (hashing), where the invariants hold by construction.
    private init(uncheckedTenant tenant: String, algorithm: String, digest: String, size: UInt64) {
        self.tenant = tenant
        self.algorithm = algorithm
        self.digest = digest
        self.size = size
    }

    /// Build a content reference from complete bytes using SHA-256.
    /// Mirrors Rust `ContentRef::for_sha256`.
    public static func forSha256(tenant: String, bytes: [UInt8]) -> ContentRef {
        var hasher = SHA256()
        hasher.update(data: Data(bytes))
        return ContentRef(
            uncheckedTenant: tenant,
            algorithm: "sha256",
            digest: hexDigest(hasher.finalize()),
            size: UInt64(bytes.count)
        )
    }

    /// Content-addressed on-disk key: `{algorithm}-{lowercased digest}`.
    /// Mirrors Rust `ContentRef::storage_key`.
    public var storageKey: String {
        "\(algorithm)-\(digest.lowercased())"
    }

    public static func < (lhs: ContentRef, rhs: ContentRef) -> Bool {
        // Lexicographic over (tenant, algorithm, digest, size) — matches the
        // Rust `#[derive(Ord)]` field order, which the cache relies on.
        if lhs.tenant != rhs.tenant { return lhs.tenant < rhs.tenant }
        if lhs.algorithm != rhs.algorithm { return lhs.algorithm < rhs.algorithm }
        if lhs.digest != rhs.digest { return lhs.digest < rhs.digest }
        return lhs.size < rhs.size
    }

    static func hexDigest(_ digest: SHA256.Digest) -> String {
        digest.map { String(format: "%02x", $0) }.joined()
    }
}

/// A minimal append sink standing in for Rust's `&mut dyn Write`, so a
/// `ContentSource` can stream bytes without materializing the whole object in
/// the caller. The verifying cache (Phase 3) will wrap this to check size +
/// digest as bytes flow through.
public protocol ContentWriter: AnyObject {
    func write(_ bytes: [UInt8]) throws
}

/// In-memory collecting writer (tests, small objects).
public final class ByteSink: ContentWriter {
    public private(set) var bytes: [UInt8] = []
    public init() {}
    public func write(_ bytes: [UInt8]) throws { self.bytes.append(contentsOf: bytes) }
}

/// Storage-neutral source of immutable complete objects.
/// Port of Rust `trait ContentSource`.
public protocol ContentSource: Sendable {
    func fetch(_ reference: ContentRef, into output: ContentWriter) throws
}

/// Port of Rust `enum FetchError`.
public enum FetchError: Error, Equatable {
    case invalidReference
    case notFound
    case cancelled
    case io(String)
    case wrongSize(expected: UInt64, actual: UInt64)
    case wrongDigest(expected: String, actual: String)
}

extension UInt8 {
    var isAsciiAlphanumeric: Bool {
        (self >= 0x30 && self <= 0x39) || (self >= 0x41 && self <= 0x5A) || (self >= 0x61 && self <= 0x7A)
    }
    var isAsciiHexDigit: Bool {
        (self >= 0x30 && self <= 0x39) || (self >= 0x41 && self <= 0x46) || (self >= 0x61 && self <= 0x66)
    }
}
