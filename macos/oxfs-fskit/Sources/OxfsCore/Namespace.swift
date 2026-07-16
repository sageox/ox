/// Port of `crates/oxfs/src/namespace.rs`.
///
/// A `Namespace` is an immutable generation of the visible tree. Readers hold a
/// reference to a generation while a newly reconciled one is built beside them
/// and swapped in (RCU). `Node` is a class (reference type) so sharing a
/// generation is cheap, mirroring the Rust `Arc<Node>`.

public enum NodeKind: Sendable {
    case directory
    case file
}

/// Why a selected path is — or is not — visible in the mount. `available`
/// means resident and shown; every other value is a reason the path is absent
/// from the tree but still recorded in `.sageox/INDEX`. The string tokens are
/// contract — tools parse them.
public enum Status: String, Sendable {
    case available = "available"
    case notSelected = "not_selected"
    case exceedsCacheLimit = "exceeds_cache_limit"
    case noSpace = "no_space"
    case authExpired = "auth_expired"
    case pathCollision = "path_collision"
}

public struct Selector: Equatable, Sendable {
    public let sessionID: String
    public let reason: String
    /// This session's own view of the path — usually equal to the aggregate
    /// status, but distinct when a per-selector reason applies (e.g. the losing
    /// side of a canonical-path collision carries `pathCollision` while the
    /// winner stays `available`).
    public let status: Status

    public init(sessionID: String, reason: String, status: Status) {
        self.sessionID = sessionID
        self.reason = reason
        self.status = status
    }
}

public struct FileNode: Sendable {
    public let content: ContentRef
    public let sourceID: String
    public let sourceKind: String
    public let reason: String
    public let selectors: [Selector]
    /// Bytes for a synthesized in-tree file (e.g. `.sageox/INDEX.md`); nil for
    /// a normal cached object served from the content store.
    public let synthetic: [UInt8]?

    public init(
        content: ContentRef,
        sourceID: String,
        sourceKind: String,
        reason: String,
        selectors: [Selector],
        synthetic: [UInt8]? = nil
    ) {
        self.content = content
        self.sourceID = sourceID
        self.sourceKind = sourceKind
        self.reason = reason
        self.selectors = selectors
        self.synthetic = synthetic
    }
}

public final class Node {
    public let inode: UInt64
    public let name: String
    public let path: String
    public let parent: UInt64
    public let kind: NodeKind
    public let mode: UInt32
    public let size: UInt64
    public let mtimeSecs: UInt64
    public let file: FileNode?
    /// name -> child inode. Iterate via `sortedChildren` for stable READDIR order.
    public internal(set) var children: [String: UInt64]

    public init(
        inode: UInt64,
        name: String,
        path: String,
        parent: UInt64,
        kind: NodeKind,
        mode: UInt32,
        size: UInt64,
        mtimeSecs: UInt64,
        file: FileNode?,
        children: [String: UInt64] = [:]
    ) {
        self.inode = inode
        self.name = name
        self.path = path
        self.parent = parent
        self.kind = kind
        self.mode = mode
        self.size = size
        self.mtimeSecs = mtimeSecs
        self.file = file
        self.children = children
    }

    public static func root() -> Node {
        Node(
            inode: ROOT_INODE,
            name: "",
            path: "",
            parent: ROOT_INODE,
            kind: .directory,
            mode: 0o555,
            size: 0,
            mtimeSecs: 0,
            file: nil,
            children: [:]
        )
    }

    /// Children in name order — the enumeration order READDIR must present.
    public var sortedChildren: [(name: String, inode: UInt64)] {
        children.sorted { $0.key < $1.key }.map { (name: $0.key, inode: $0.value) }
    }
}

/// Immutable namespace generation.
public final class Namespace {
    public let generation: UInt64
    public private(set) var nodes: [UInt64: Node]
    public private(set) var byPathIndex: [String: UInt64]

    init(generation: UInt64, nodes: [UInt64: Node], byPathIndex: [String: UInt64]) {
        self.generation = generation
        self.nodes = nodes
        self.byPathIndex = byPathIndex
    }

    public static func empty() -> Namespace {
        let root = Node.root()
        return Namespace(
            generation: 0,
            nodes: [ROOT_INODE: root],
            byPathIndex: ["": ROOT_INODE]
        )
    }

    public func get(_ inode: UInt64) -> Node? {
        nodes[inode]
    }

    public func lookup(parent: UInt64, name: String) -> Node? {
        guard let childInode = nodes[parent]?.children[name] else { return nil }
        return nodes[childInode]
    }

    public func byPath(_ path: String) -> Node? {
        guard let inode = byPathIndex[path] else { return nil }
        return nodes[inode]
    }
}
