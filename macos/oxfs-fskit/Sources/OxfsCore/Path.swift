/// A validated, safe relative path within the oxFS namespace.
/// Port of `crates/oxfs/src/path.rs::RelativePath`.
///
/// Rejects: empty, leading/trailing `/`, empty components, `.`/`..`, embedded
/// NUL, and anything under the reserved `.sageox` namespace (which oxFS
/// synthesizes itself).
public struct RelativePath: Hashable, Comparable, Sendable {
    private let value: String

    public static func parse(_ value: String) throws -> RelativePath {
        if value.isEmpty || value.hasPrefix("/") || value.hasSuffix("/") {
            throw PathError.invalid(value)
        }
        for component in value.split(separator: "/", omittingEmptySubsequences: false) {
            if component.isEmpty || component == "." || component == ".." {
                throw PathError.invalid(value)
            }
            if component.utf8.contains(0) {
                throw PathError.invalid(value)
            }
        }
        if value == ".sageox" || value.hasPrefix(".sageox/") {
            throw PathError.reserved(value)
        }
        return RelativePath(value: value)
    }

    public var asString: String { value }

    public func components() -> [String] {
        value.split(separator: "/").map(String.init)
    }

    public var fileName: String {
        String(value.split(separator: "/").last!)
    }

    /// The parent path prefix, or nil for a top-level entry.
    public var parent: String? {
        guard let idx = value.lastIndex(of: "/") else { return nil }
        return String(value[value.startIndex..<idx])
    }

    public static func < (lhs: RelativePath, rhs: RelativePath) -> Bool {
        lhs.value < rhs.value
    }
}

/// Port of Rust `enum PathError`.
public enum PathError: Error, Equatable {
    case invalid(String)
    case reserved(String)
}

// Custom coder (validates on decode) — declared here so `ManifestEntry`/`Manifest`
// can synthesize their own Codable conformances.
extension RelativePath: Codable {
    public init(from decoder: Decoder) throws {
        let raw = try decoder.singleValueContainer().decode(String.self)
        self = try RelativePath.parse(raw)
    }
    public func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        try container.encode(asString)
    }
}
