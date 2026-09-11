use crate::content::ContentRef;
use crate::inode::ROOT_INODE;
use std::collections::BTreeMap;
use std::sync::Arc;

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum NodeKind {
    Directory,
    File,
}

/// Why a selected path is — or is not — visible in the mount. `Available` means
/// resident and shown; every other value is a reason the path is absent from
/// the tree but still recorded in `.sageox/INDEX` (design §3a). Two of these are
/// **per-selector** — evaluated against an individual selecting Session:
/// `NotSelected` and `AuthExpired` (the latter against that Session's own ox
/// token, which does not exist in the crate yet — no code path produces it).
/// The rest are **per-path** materialization outcomes that apply to every
/// selector of the path: `ExceedsCacheLimit`, `NoSpace`, and `PathCollision`.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Status {
    Available,
    NotSelected,
    ExceedsCacheLimit,
    NoSpace,
    AuthExpired,
    PathCollision,
}

impl Status {
    /// The stable token written into `.sageox/INDEX.{md,json}`. These are the
    /// enum values named in the design; tools parse them, so they are contract.
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Available => "available",
            Self::NotSelected => "not_selected",
            Self::ExceedsCacheLimit => "exceeds_cache_limit",
            Self::NoSpace => "no_space",
            Self::AuthExpired => "auth_expired",
            Self::PathCollision => "path_collision",
        }
    }
}

#[derive(Clone, Debug)]
pub struct FileNode {
    pub content: ContentRef,
    pub source_id: String,
    pub source_kind: String,
    pub reason: String,
    pub selectors: Vec<Selector>,
    pub synthetic: Option<Arc<[u8]>>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Selector {
    pub session_id: String,
    pub reason: String,
    /// This Session's own view of the path. Usually equal to the path's
    /// aggregate status, but distinct when a per-selector reason applies — a
    /// losing entry in a canonical-path collision carries `PathCollision` while
    /// the winning selector stays `Available` (see `Workspace::build_namespace`).
    pub status: Status,
}

#[derive(Clone, Debug)]
pub struct Node {
    pub inode: u64,
    pub name: String,
    pub path: String,
    pub parent: u64,
    pub kind: NodeKind,
    pub mode: u32,
    pub size: u64,
    pub mtime_secs: u64,
    pub file: Option<FileNode>,
    pub children: BTreeMap<String, u64>,
}

impl Node {
    pub fn root() -> Self {
        Self {
            inode: ROOT_INODE,
            name: String::new(),
            path: String::new(),
            parent: ROOT_INODE,
            kind: NodeKind::Directory,
            mode: 0o555,
            size: 0,
            mtime_secs: 0,
            file: None,
            children: BTreeMap::new(),
        }
    }
}

/// Immutable namespace generation. Readers retain an `Arc` while a newly
/// reconciled generation is built away from them and swapped by the workspace.
#[derive(Clone, Debug)]
pub struct Namespace {
    pub generation: u64,
    pub nodes: BTreeMap<u64, Arc<Node>>,
    pub by_path: BTreeMap<String, u64>,
}

impl Namespace {
    pub fn empty() -> Self {
        Self {
            generation: 0,
            nodes: BTreeMap::from([(ROOT_INODE, Arc::new(Node::root()))]),
            by_path: BTreeMap::from([(String::new(), ROOT_INODE)]),
        }
    }

    pub fn get(&self, inode: u64) -> Option<&Arc<Node>> {
        self.nodes.get(&inode)
    }

    pub fn lookup(&self, parent: u64, name: &str) -> Option<&Arc<Node>> {
        let inode = self.nodes.get(&parent)?.children.get(name)?;
        self.nodes.get(inode)
    }

    pub fn by_path(&self, path: &str) -> Option<&Arc<Node>> {
        self.nodes.get(self.by_path.get(path)?)
    }
}
