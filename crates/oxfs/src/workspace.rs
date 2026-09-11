use crate::cache::{CacheConfig, ContentCache, read_range};
use crate::cache_policy::{AdmissionCandidate, AdmitEverything};
use crate::content::{ContentSource, FetchError};
use crate::inode::{InodeTable, ROOT_INODE};
use crate::manifest::{Manifest, ManifestError};
use crate::namespace::{FileNode, Namespace, Node, NodeKind, Selector, Status};
use crate::observations::{ObservationKind, ObservationLog};
use crate::selections::SelectionStore;
use std::collections::{BTreeMap, BTreeSet};
use std::fmt;
use std::fs::File;
use std::io;
use std::path::Path;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, RwLock};

/// Per-op counts for the NFS surface, split by whether the op touches a cache
/// object. `read` is the only data op: it resolves a `ContentRef` and opens the
/// backing blob (bumping the cache's `opened_objects`). Everything else —
/// `lookup`, `getattr`, `access`, `readdir` — is served entirely from the
/// namespace snapshot and inode table and never reaches the content store. This
/// is why `cd`/`ls` move none of the `*_objects` counters: they are pure
/// metadata traffic that, until now, emitted no telemetry at all.
#[derive(Default)]
struct NfsTelemetry {
    lookup: AtomicU64,
    getattr: AtomicU64,
    access: AtomicU64,
    readdir: AtomicU64,
    read: AtomicU64,
}

#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
pub struct NfsTelemetrySnapshot {
    pub lookup: u64,
    pub getattr: u64,
    pub access: u64,
    pub readdir: u64,
    pub read: u64,
}

pub struct Workspace {
    namespace: RwLock<Arc<Namespace>>,
    inodes: Mutex<InodeTable>,
    cache: ContentCache,
    observations: ObservationLog,
    sessions: Mutex<BTreeMap<String, Manifest>>,
    selections: SelectionStore,
    reconcile: Mutex<()>,
    nfs: NfsTelemetry,
}

#[derive(Clone)]
struct DesiredIndex {
    /// The entry that binds the path — the "winner". A path is visible only when
    /// this entry's content is resident; losing entries in a canonical-path
    /// collision are recorded as extra selectors, never bound here.
    entry: crate::manifest::ManifestEntry,
    selectors: Vec<Selector>,
    /// The per-path aggregate status: `Available` iff the winning entry is
    /// resident, otherwise the governing non-visible reason (design §Insight).
    status: Status,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct ApplyOutcome {
    pub applied: bool,
    pub available: usize,
    pub stopped: usize,
}

fn resident_desired_keys<'a>(
    cache: &ContentCache,
    manifests: impl Iterator<Item = &'a Manifest>,
) -> Result<BTreeSet<String>, WorkspaceError> {
    let mut resident = BTreeSet::new();
    for entry in manifests.flat_map(|manifest| &manifest.entries) {
        if cache.resident(&entry.content)?.is_some() {
            resident.insert(cache.storage_key(&entry.content));
        }
    }
    Ok(resident)
}

impl Workspace {
    pub fn open(root: &Path, source: Arc<dyn ContentSource>) -> Result<Arc<Self>, WorkspaceError> {
        Self::open_with_config(root, source, CacheConfig::default())
    }

    pub fn open_with_config(
        root: &Path,
        source: Arc<dyn ContentSource>,
        config: CacheConfig,
    ) -> Result<Arc<Self>, WorkspaceError> {
        let state = root.join("state");
        let observations = ObservationLog::open(&state)?;
        let selections = SelectionStore::open(&state)?;
        let sessions = selections.load().unwrap_or_default();
        let workspace = Arc::new(Self {
            namespace: RwLock::new(Arc::new(Namespace::empty())),
            inodes: Mutex::new(InodeTable::open(&state)?),
            cache: ContentCache::open(root.join("cache"), source, config)?,
            observations,
            sessions: Mutex::new(sessions),
            selections,
            reconcile: Mutex::new(()),
            nfs: NfsTelemetry::default(),
        });
        // Restore as much of the persisted desired set as the configured cap
        // permits. Corrupt objects are removed by `resident` before admission.
        let protected: BTreeMap<_, _> = {
            let sessions = workspace
                .sessions
                .lock()
                .map_err(|_| WorkspaceError::Poisoned)?;
            sessions
                .values()
                .flat_map(|m| m.entries.iter())
                .filter(|e| {
                    workspace
                        .cache
                        .resident(&e.content)
                        .ok()
                        .flatten()
                        .is_some()
                })
                .map(|e| (workspace.cache.storage_key(&e.content), e.content.size))
                .collect()
        };
        let mut available: BTreeSet<_> = protected.keys().cloned().collect();
        {
            let sessions = workspace
                .sessions
                .lock()
                .map_err(|_| WorkspaceError::Poisoned)?;
            let admissions: Vec<_> = sessions
                .values()
                .flat_map(|manifest| manifest.entries.iter().map(|entry| entry.content.clone()))
                .collect();
            workspace.cache.begin_batch(&admissions)?;
            let mut stopped = false;
            for entry in sessions.values().flat_map(|m| m.entries.iter()) {
                let key = workspace.cache.storage_key(&entry.content);
                if available.contains(&key) {
                    continue;
                }
                if stopped || entry.content.size > workspace.cache.capacity() {
                    stopped = true;
                    continue;
                }
                match workspace.cache.materialize_missing(&entry.content) {
                    Ok(_) => {
                        available.insert(key);
                    }
                    Err(FetchError::Io(error)) if error.kind() == io::ErrorKind::StorageFull => {
                        stopped = true
                    }
                    Err(_) => stopped = true,
                }
            }
            workspace.cache.commit_batch()?;
        }
        let recovered = {
            let sessions = workspace
                .sessions
                .lock()
                .map_err(|_| WorkspaceError::Poisoned)?;
            workspace.build_namespace_recovered(&sessions, &available)?
        };
        *workspace
            .namespace
            .write()
            .map_err(|_| WorkspaceError::Poisoned)? = Arc::new(recovered);
        Ok(workspace)
    }

    pub fn snapshot(&self) -> Arc<Namespace> {
        self.namespace
            .read()
            .expect("namespace lock poisoned")
            .clone()
    }

    /// Apply a complete per-session desired set. Fetch and verification happen
    /// before the replacement namespace is made visible.
    pub fn apply(&self, manifest: Manifest) -> Result<ApplyOutcome, WorkspaceError> {
        let _reconcile = self
            .reconcile
            .lock()
            .map_err(|_| WorkspaceError::Poisoned)?;
        manifest.validate()?;
        {
            let sessions = self.sessions.lock().map_err(|_| WorkspaceError::Poisoned)?;
            if let Some(current) = sessions.get(&manifest.session_id)
                && manifest.generation <= current.generation
            {
                return Ok(ApplyOutcome {
                    applied: false,
                    available: 0,
                    stopped: 0,
                });
            }
        }
        let mut sessions = self.sessions.lock().map_err(|_| WorkspaceError::Poisoned)?;
        if sessions
            .get(&manifest.session_id)
            .is_some_and(|current| manifest.generation <= current.generation)
        {
            return Ok(ApplyOutcome {
                applied: false,
                available: 0,
                stopped: 0,
            });
        }
        let mut candidate = sessions.clone();
        let admissions = manifest.entries.clone();
        candidate.insert(manifest.session_id.clone(), manifest);
        // A canonical-path collision (two Sessions selecting the same path with
        // different content) is handled per entry, not by rejecting the whole
        // update: `build_namespace` binds the winning entry, keeps the rest of
        // the working set, and records the losing selectors as `path_collision`.
        let mut protected = BTreeMap::new();
        let mut available = BTreeSet::new();
        for entry in candidate.values().flat_map(|manifest| &manifest.entries) {
            if self.cache.resident(&entry.content)?.is_some() {
                let key = self.cache.storage_key(&entry.content);
                protected.insert(key.clone(), entry.content.size);
                available.insert(key);
            }
        }
        // The old namespace remains published until the replacement is fully
        // durable. Pin all of its backing objects through this reconciliation,
        // including objects the candidate deliberately drops.
        let published = self.snapshot();
        for file in published
            .nodes
            .values()
            .filter_map(|node| node.file.as_ref())
            .filter(|file| file.synthetic.is_none())
        {
            let key = self.cache.storage_key(&file.content);
            if !protected.contains_key(&key) && self.cache.resident(&file.content)?.is_some() {
                protected.insert(key, file.content.size);
            }
        }
        // Peak cache demand for this reconciliation is every unique object the
        // candidate needs plus every resident object the still-published
        // namespace needs and the candidate drops: the old namespace stays
        // readable until the swap, so both sets are pinned at once. A candidate
        // that fits the cache on its own but not beside the live selection is a
        // transient replacement squeeze, not an oversized selection. Admitting
        // it partially would silently publish a subset of what the caller asked
        // for, so refuse before the first transfer and leave the live namespace
        // exactly as it is.
        let mut candidate_bytes = BTreeMap::new();
        for entry in candidate.values().flat_map(|manifest| &manifest.entries) {
            candidate_bytes.insert(self.cache.storage_key(&entry.content), entry.content.size);
        }
        let needed: u64 = candidate_bytes.values().sum();
        let pinned: u64 = protected
            .iter()
            .filter(|(key, _)| !candidate_bytes.contains_key(*key))
            .map(|(_, size)| *size)
            .sum();
        let capacity = self.cache.capacity();
        if needed <= capacity && needed.saturating_add(pinned) > capacity {
            return Err(WorkspaceError::ReplacementCapacity {
                needed,
                pinned,
                capacity,
            });
        }
        let keyed: Vec<_> = admissions
            .iter()
            .map(|entry| (self.cache.storage_key(&entry.content), &entry.content))
            .collect();
        let candidates: Vec<_> = keyed
            .iter()
            .map(|(key, content)| AdmissionCandidate {
                key,
                value: *content,
                size: content.size,
            })
            .collect();
        let missing = AdmitEverything::select(self.cache.capacity(), &available, &candidates);
        let admission_content: Vec<_> = admissions
            .iter()
            .map(|entry| entry.content.clone())
            .collect();
        self.cache.begin_batch(&admission_content)?;
        match self.cache.materialize_missing_batch(&missing) {
            Ok(materialized) => {
                available.extend(materialized);
            }
            Err(error) => {
                let _ = self.cache.rollback_batch();
                return Err(error.into());
            }
        }
        let _next = match self.build_namespace(&candidate, &available) {
            Ok(next) => next,
            Err(error) => {
                let _ = self.cache.rollback_batch();
                return Err(error);
            }
        };
        if let Err(error) = self.cache.commit_batch() {
            let _ = self.cache.rollback_batch();
            return Err(error.into());
        }
        available = resident_desired_keys(&self.cache, candidate.values())?;
        let next = self.build_namespace(&candidate, &available)?;
        let available = next
            .nodes
            .values()
            .filter(|n| {
                n.kind == NodeKind::File && n.file.as_ref().is_some_and(|f| f.synthetic.is_none())
            })
            .count();
        let desired_paths: std::collections::BTreeSet<_> = candidate
            .values()
            .flat_map(|m| m.entries.iter().map(|e| e.path.as_str()))
            .collect();
        let stopped_count = desired_paths.len().saturating_sub(available);
        self.selections.replace(&candidate)?;
        *sessions = candidate;
        *self
            .namespace
            .write()
            .map_err(|_| WorkspaceError::Poisoned)? = Arc::new(next);
        Ok(ApplyOutcome {
            applied: true,
            available,
            stopped: stopped_count,
        })
    }

    fn build_namespace(
        &self,
        sessions: &BTreeMap<String, Manifest>,
        available: &BTreeSet<String>,
    ) -> Result<Namespace, WorkspaceError> {
        // Group every selecting entry by canonical path. This is where the union
        // across Sessions is reconciled: a path with more than one distinct
        // content is a collision, handled per entry (design §Insight). We never
        // reject the whole update — we bind the winning entry, keep the rest of
        // the working set, and record the losing selectors as `path_collision`.
        let mut grouped: BTreeMap<String, Vec<(String, &crate::manifest::ManifestEntry)>> =
            BTreeMap::new();
        let mut max_generation = 0;
        for manifest in sessions.values() {
            max_generation = max_generation.max(manifest.generation);
            for entry in &manifest.entries {
                grouped
                    .entry(entry.path.as_str().to_owned())
                    .or_default()
                    .push((manifest.session_id.clone(), entry));
            }
        }
        let mut desired: BTreeMap<String, DesiredIndex> = BTreeMap::new();
        let published = self.snapshot();
        for (path, entries) in &grouped {
            // Prefer a winner whose content is already resident so a refresh
            // never hides a visible file behind a conflicting selection ("never
            // silently overwrites the resident file"). Break ties deterministically
            // by Session-sorted order (`entries[0]` is the lowest Session ID).
            let winner = entries
                .iter()
                .find(|(_, entry)| {
                    published
                        .by_path(path)
                        .and_then(|node| node.file.as_ref())
                        .is_some_and(|file| file.content == entry.content)
                        && available.contains(&self.cache.storage_key(&entry.content))
                })
                .or_else(|| {
                    entries.iter().find(|(_, entry)| {
                        available.contains(&self.cache.storage_key(&entry.content))
                    })
                })
                .map_or(entries[0].1, |(_, entry)| *entry);
            let collided = entries
                .iter()
                .any(|(_, entry)| entry.content != winner.content);
            let winner_resident = available.contains(&self.cache.storage_key(&winner.content));
            // Per-path aggregate: `available` if the winner is resident, else the
            // governing non-visible reason. Collision outranks capacity reasons
            // because the path can never bind while two contents contend for it.
            let path_status = if winner_resident {
                Status::Available
            } else if collided {
                Status::PathCollision
            } else if winner.content.size > self.cache.capacity() {
                Status::ExceedsCacheLimit
            } else {
                Status::NoSpace
            };
            // Per-selector: an entry contending with a *different* content is the
            // losing side of the collision and is always `path_collision`; an
            // entry sharing the winner's content shares the path's fate. (Once
            // the mount grows a per-session token, `auth_expired` becomes another
            // per-selector reason evaluated here.)
            let selectors = entries
                .iter()
                .map(|(session_id, entry)| Selector {
                    session_id: session_id.clone(),
                    reason: entry.reason.clone(),
                    status: if entry.content == winner.content {
                        path_status
                    } else {
                        Status::PathCollision
                    },
                })
                .collect();
            desired.insert(
                path.clone(),
                DesiredIndex {
                    entry: winner.clone(),
                    selectors,
                    status: path_status,
                },
            );
        }
        let mut nodes: BTreeMap<u64, Node> = BTreeMap::from([(ROOT_INODE, Node::root())]);
        let mut by_path = BTreeMap::from([(String::new(), ROOT_INODE)]);
        let mut inodes = self.inodes.lock().map_err(|_| WorkspaceError::Poisoned)?;
        let index_desired = desired.clone();
        for (path, desired) in desired {
            let entry = &desired.entry;
            // Only a resident winner is visible; `status == Available` holds
            // exactly then. A blocked or collided path stays out of the tree and
            // is explained solely through `.sageox/INDEX` (WYSIWYG, design §2).
            if desired.status != Status::Available {
                continue;
            }
            let parts: Vec<_> = path.split('/').collect();
            let mut parent = ROOT_INODE;
            let mut prefix = String::new();
            for (index, part) in parts.iter().enumerate() {
                if !prefix.is_empty() {
                    prefix.push('/');
                }
                prefix.push_str(part);
                let inode = inodes.inode_for(&prefix)?;
                if let std::collections::btree_map::Entry::Vacant(slot) = nodes.entry(inode) {
                    let is_file = index + 1 == parts.len();
                    let node = if is_file {
                        Node {
                            inode,
                            name: (*part).into(),
                            path: prefix.clone(),
                            parent,
                            kind: NodeKind::File,
                            mode: entry.mode,
                            size: entry.content.size,
                            mtime_secs: entry.mtime_secs,
                            file: Some(FileNode {
                                content: entry.content.clone(),
                                source_id: entry.source_id.clone(),
                                source_kind: entry.source_kind.clone(),
                                reason: entry.reason.clone(),
                                selectors: desired.selectors.clone(),
                                synthetic: None,
                            }),
                            children: BTreeMap::new(),
                        }
                    } else {
                        Node {
                            inode,
                            name: (*part).into(),
                            path: prefix.clone(),
                            parent,
                            kind: NodeKind::Directory,
                            mode: 0o555,
                            size: 0,
                            mtime_secs: entry.mtime_secs,
                            file: None,
                            children: BTreeMap::new(),
                        }
                    };
                    slot.insert(node);
                    by_path.insert(prefix.clone(), inode);
                    nodes
                        .get_mut(&parent)
                        .expect("parent built first")
                        .children
                        .insert((*part).into(), inode);
                }
                parent = inode;
            }
        }
        add_indexes(
            &mut nodes,
            &mut by_path,
            &mut inodes,
            max_generation,
            &index_desired,
        )?;
        // Allocate paths freely while constructing the private candidate, then
        // cross one durability boundary immediately before it can be published.
        inodes.sync()?;
        Ok(Namespace {
            generation: max_generation,
            nodes: nodes
                .into_iter()
                .map(|(ino, node)| (ino, Arc::new(node)))
                .collect(),
            by_path,
        })
    }

    fn build_namespace_recovered(
        &self,
        sessions: &BTreeMap<String, Manifest>,
        available: &BTreeSet<String>,
    ) -> Result<Namespace, WorkspaceError> {
        self.build_namespace(sessions, available)
    }

    pub fn cache_capacity(&self) -> (u64, u64) {
        (self.cache.capacity(), self.cache.remaining())
    }

    /// Cumulative (objects evicted, reservations blocked because every resident
    /// object was pinned by the live namespace or an in-flight fetch).
    pub fn eviction_counters(&self) -> (u64, u64) {
        self.cache.eviction_counters()
    }

    pub fn cache_telemetry(&self) -> io::Result<crate::cache::CacheTelemetrySnapshot> {
        self.cache.telemetry()
    }

    pub fn record_nfs_lookup(&self) {
        self.nfs.lookup.fetch_add(1, Ordering::Relaxed);
    }
    pub fn record_nfs_getattr(&self) {
        self.nfs.getattr.fetch_add(1, Ordering::Relaxed);
    }
    pub fn record_nfs_access(&self) {
        self.nfs.access.fetch_add(1, Ordering::Relaxed);
    }
    pub fn record_nfs_readdir(&self) {
        self.nfs.readdir.fetch_add(1, Ordering::Relaxed);
    }
    pub fn record_nfs_read(&self) {
        self.nfs.read.fetch_add(1, Ordering::Relaxed);
    }

    /// Cumulative NFS op counts. `read` is the only data op (touches a cache
    /// object); the rest are pure metadata served from the namespace + inode
    /// table. Pair with `cache_telemetry` to see the two surfaces side by side.
    pub fn nfs_telemetry(&self) -> NfsTelemetrySnapshot {
        NfsTelemetrySnapshot {
            lookup: self.nfs.lookup.load(Ordering::Relaxed),
            getattr: self.nfs.getattr.load(Ordering::Relaxed),
            access: self.nfs.access.load(Ordering::Relaxed),
            readdir: self.nfs.readdir.load(Ordering::Relaxed),
            read: self.nfs.read.load(Ordering::Relaxed),
        }
    }

    pub fn resident_keys(&self) -> io::Result<Vec<String>> {
        self.cache.resident_keys()
    }

    pub fn storage_key(&self, reference: &crate::content::ContentRef) -> String {
        self.cache.storage_key(reference)
    }

    pub fn snapshot_state(&self) -> io::Result<crate::cache_catalog::CatalogSnapshot> {
        self.cache.snapshot_state()
    }

    pub fn open_inode(self: &Arc<Self>, inode: u64) -> Result<OpenFile, WorkspaceError> {
        let node = self
            .snapshot()
            .get(inode)
            .cloned()
            .ok_or(WorkspaceError::NotFound)?;
        if node.kind != NodeKind::File {
            return Err(WorkspaceError::IsDirectory);
        }
        let file_node = node.file.as_ref().expect("file node content");
        let backing = match &file_node.synthetic {
            Some(bytes) => OpenBacking::Inline(bytes.clone()),
            None => OpenBacking::File(self.cache.open_file(&file_node.content)?),
        };
        self.observations
            .append(ObservationKind::Open, inode, &node.path, 0, 0)?;
        Ok(OpenFile {
            node,
            backing,
            workspace: Arc::clone(self),
        })
    }

    pub fn dismiss(&self, inode: u64) -> Result<(), WorkspaceError> {
        let node = self
            .snapshot()
            .get(inode)
            .cloned()
            .ok_or(WorkspaceError::NotFound)?;
        self.observations
            .append(ObservationKind::Dismiss, inode, &node.path, 0, 0)?;
        Ok(())
    }
}

// Keeping the Workspace alive pins both the cache owner and observation log.
pub struct OpenFile {
    node: Arc<Node>,
    backing: OpenBacking,
    workspace: Arc<Workspace>,
}

enum OpenBacking {
    File(File),
    Inline(Arc<[u8]>),
}

impl OpenFile {
    pub fn inode(&self) -> u64 {
        self.node.inode
    }
    pub fn size(&self) -> u64 {
        self.node.size
    }
    pub fn read(&self, offset: u64, count: usize) -> Result<Vec<u8>, WorkspaceError> {
        let data = match &self.backing {
            OpenBacking::File(file) => read_range(file, offset, count)?,
            OpenBacking::Inline(bytes) => {
                let start = usize::try_from(offset)
                    .unwrap_or(usize::MAX)
                    .min(bytes.len());
                let end = start.saturating_add(count).min(bytes.len());
                bytes[start..end].to_vec()
            }
        };
        self.workspace.observations.append(
            ObservationKind::Read,
            self.node.inode,
            &self.node.path,
            offset,
            data.len(),
        )?;
        Ok(data)
    }
}

fn add_indexes(
    nodes: &mut BTreeMap<u64, Node>,
    by_path: &mut BTreeMap<String, u64>,
    inodes: &mut InodeTable,
    generation: u64,
    desired: &BTreeMap<String, DesiredIndex>,
) -> Result<(), WorkspaceError> {
    let dir_inode = inodes.inode_for(".sageox")?;
    let md_inode = inodes.inode_for(".sageox/INDEX.md")?;
    let json_inode = inodes.inode_for(".sageox/INDEX.json")?;
    let mut markdown = format!(
        "# oxFS working set\n\nGeneration: `{generation}`\n\n| Path | Size | Kind | Status | Selected by | Why |\n|---|---:|---|---|---|---|\n"
    );
    let mut json = format!("{{\"generation\":{generation},\"files\":[");
    for (index, (path, item)) in desired.iter().enumerate() {
        let file = &item.entry;
        // The per-path aggregate; each selector below carries its own status too.
        let status = item.status.as_str();
        let selectors = item
            .selectors
            .iter()
            .map(|selector| selector.session_id.as_str())
            .collect::<Vec<_>>()
            .join(", ");
        let reasons = item
            .selectors
            .iter()
            .map(|selector| selector.reason.as_str())
            .collect::<Vec<_>>()
            .join("; ");
        markdown.push_str(&format!(
            "| `{}` | {} | {} | {} | {} | {} |\n",
            markdown_escape(path),
            file.content.size,
            markdown_escape(&file.source_kind),
            status,
            markdown_escape(&selectors),
            markdown_escape(&reasons)
        ));
        if index != 0 {
            json.push(',');
        }
        json.push_str(&format!(
            "{{\"path\":\"{}\",\"size\":{},\"source_id\":\"{}\",\"source_kind\":\"{}\",\"status\":\"{}\",\"selectors\":[",
            json_escape(path), file.content.size, json_escape(&file.source_id), json_escape(&file.source_kind), status
        ));
        for (selector_index, selector) in item.selectors.iter().enumerate() {
            if selector_index != 0 {
                json.push(',');
            }
            json.push_str(&format!(
                "{{\"session_id\":\"{}\",\"reason\":\"{}\",\"status\":\"{}\"}}",
                json_escape(&selector.session_id),
                json_escape(&selector.reason),
                selector.status.as_str()
            ));
        }
        json.push_str("]}");
    }
    json.push_str("]}\n");

    let directory = Node {
        inode: dir_inode,
        name: ".sageox".into(),
        path: ".sageox".into(),
        parent: ROOT_INODE,
        kind: NodeKind::Directory,
        mode: 0o555,
        size: 0,
        mtime_secs: 0,
        file: None,
        children: BTreeMap::from([
            ("INDEX.json".into(), json_inode),
            ("INDEX.md".into(), md_inode),
        ]),
    };
    nodes
        .get_mut(&ROOT_INODE)
        .expect("root exists")
        .children
        .insert(".sageox".into(), dir_inode);
    nodes.insert(dir_inode, directory);
    by_path.insert(".sageox".into(), dir_inode);

    let synthetic_ref = crate::ContentRef::new("synthetic", "sha256", "00", 0)
        .expect("static synthetic reference is valid");
    for (inode, name, bytes) in [
        (md_inode, "INDEX.md", markdown.into_bytes()),
        (json_inode, "INDEX.json", json.into_bytes()),
    ] {
        let path = format!(".sageox/{name}");
        let node = Node {
            inode,
            name: name.into(),
            path: path.clone(),
            parent: dir_inode,
            kind: NodeKind::File,
            mode: 0o444,
            size: bytes.len() as u64,
            mtime_secs: 0,
            file: Some(FileNode {
                content: synthetic_ref.clone(),
                source_id: "oxfs".into(),
                source_kind: "Index".into(),
                reason: "mount-global working-set index".into(),
                selectors: vec![],
                synthetic: Some(Arc::from(bytes)),
            }),
            children: BTreeMap::new(),
        };
        nodes.insert(inode, node);
        by_path.insert(path, inode);
    }
    Ok(())
}

fn markdown_escape(value: &str) -> String {
    value.replace('|', "\\|").replace('\n', " ")
}

fn json_escape(value: &str) -> String {
    let mut output = String::with_capacity(value.len());
    for character in value.chars() {
        match character {
            '"' => output.push_str("\\\""),
            '\\' => output.push_str("\\\\"),
            '\n' => output.push_str("\\n"),
            '\r' => output.push_str("\\r"),
            '\t' => output.push_str("\\t"),
            value if value < ' ' => {
                use std::fmt::Write as _;
                write!(output, "\\u{:04x}", value as u32).expect("write to String");
            }
            value => output.push(value),
        }
    }
    output
}

#[derive(Debug)]
pub enum WorkspaceError {
    Io(io::Error),
    Fetch(FetchError),
    Manifest(ManifestError),
    NotFound,
    IsDirectory,
    ReadOnly,
    /// The candidate fits the cache alone, but not while the still-published
    /// namespace keeps its own objects pinned through the swap.
    ReplacementCapacity {
        needed: u64,
        pinned: u64,
        capacity: u64,
    },
    Poisoned,
}
impl fmt::Display for WorkspaceError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Io(e) => write!(f, "workspace I/O: {e}"),
            Self::Fetch(e) => write!(f, "fetch: {e}"),
            Self::Manifest(e) => write!(f, "manifest: {e}"),
            Self::NotFound => write!(f, "not found"),
            Self::IsDirectory => write!(f, "is a directory"),
            Self::ReadOnly => write!(f, "read-only filesystem"),
            Self::ReplacementCapacity {
                needed,
                pinned,
                capacity,
            } => write!(
                f,
                "selection needs {needed} bytes and the still-published selection pins {pinned} more \
                 through the swap, exceeding the {capacity} byte cache; drop the current selection \
                 first or raise the cache capacity"
            ),
            Self::Poisoned => write!(f, "workspace lock poisoned"),
        }
    }
}
impl std::error::Error for WorkspaceError {}
impl From<io::Error> for WorkspaceError {
    fn from(v: io::Error) -> Self {
        Self::Io(v)
    }
}
impl From<FetchError> for WorkspaceError {
    fn from(v: FetchError) -> Self {
        Self::Fetch(v)
    }
}
impl From<ManifestError> for WorkspaceError {
    fn from(v: ManifestError) -> Self {
        Self::Manifest(v)
    }
}
