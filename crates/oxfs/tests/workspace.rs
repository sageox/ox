use oxfs::{ContentRef, ContentSource, FetchError, Manifest, ManifestEntry, Workspace};
use std::collections::BTreeMap;
use std::io::Write;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

struct MemorySource {
    objects: BTreeMap<String, Vec<u8>>,
}
impl ContentSource for MemorySource {
    fn fetch(&self, r: &ContentRef, w: &mut dyn Write) -> Result<(), FetchError> {
        let bytes = self.objects.get(&r.digest).ok_or(FetchError::NotFound)?;
        w.write_all(bytes)?;
        Ok(())
    }
}
fn temp(name: &str) -> std::path::PathBuf {
    static NEXT: AtomicUsize = AtomicUsize::new(0);
    std::env::temp_dir().join(format!(
        "oxfs-{name}-{}-{}-{}",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ))
}
fn reference() -> ContentRef {
    ContentRef::new(
        "t",
        "sha256",
        "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
        3,
    )
    .unwrap()
}

#[test]
fn content_algorithm_cannot_escape_the_cache_namespace() {
    assert!(ContentRef::new("tenant", "../sha256", "00", 1).is_err());
    assert!(ContentRef::new("tenant", "sha256/other", "00", 1).is_err());
    assert!(ContentRef::new("tenant", "blake3-preview_1", "00", 1).is_ok());
}

fn source(bytes: &[u8]) -> Arc<dyn ContentSource> {
    Arc::new(MemorySource {
        objects: BTreeMap::from([(reference().digest, bytes.to_vec())]),
    })
}
fn manifest(generation: u64) -> Manifest {
    Manifest {
        session_id: "s1".into(),
        generation,
        entries: vec![
            ManifestEntry::new(
                "sessions/one/raw.jsonl",
                "src",
                "Session",
                0o644,
                123,
                reference(),
                "related",
            )
            .unwrap(),
        ],
    }
}

#[test]
fn wysiwyg_and_stable_restart() {
    let root = temp("restart");
    let ws = Workspace::open(&root, source(b"abc")).unwrap();
    assert!(ws.snapshot().by_path("sessions/one/raw.jsonl").is_none());
    ws.apply(manifest(1)).unwrap();
    let before = ws
        .snapshot()
        .by_path("sessions/one/raw.jsonl")
        .unwrap()
        .inode;
    assert_eq!(ws.open_inode(before).unwrap().read(0, 99).unwrap(), b"abc");
    drop(ws);
    let ws = Workspace::open(&root, source(b"abc")).unwrap();
    assert_eq!(
        before,
        ws.snapshot()
            .by_path("sessions/one/raw.jsonl")
            .unwrap()
            .inode
    );
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn sqlite_catalog_is_persistent_and_telemetry_is_observable() {
    let root = temp("catalog");
    let ws = Workspace::open(&root, source(b"abc")).unwrap();
    ws.apply(manifest(1)).unwrap();
    let telemetry = ws.cache_telemetry().unwrap();
    assert_eq!(telemetry.fetched_objects, 1);
    assert_eq!(telemetry.fetched_bytes, 3);
    assert_eq!(telemetry.resident_objects, 1);
    assert_eq!(telemetry.resident_bytes, 3);
    assert_eq!(telemetry.pending_objects, 0);
    assert_eq!(telemetry.catalog_transactions, 2); // empty restore + generation 1
    assert_eq!(telemetry.directory_syncs, 1);
    assert!(root.join("cache/catalog.sqlite").is_file());
    assert!(!root.join("cache/metadata.v1").exists());
    assert!(!root.join("cache/active-apply.v1").exists());

    drop(ws);
    let ws = Workspace::open(&root, source(b"abc")).unwrap();
    assert_eq!(ws.cache_telemetry().unwrap().resident_objects, 1);
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn manifest_batch_materializes_unique_objects_and_syncs_directory_once() {
    let root = temp("parallel-batch");
    let mut objects = BTreeMap::new();
    let mut entries = Vec::new();
    for index in 0..64 {
        let bytes = format!("object-{index}").into_bytes();
        let reference = ContentRef::for_sha256("t", &bytes);
        objects.insert(reference.digest.clone(), bytes);
        entries.push(
            ManifestEntry::new(
                format!("objects/{index}"),
                "src",
                "Session",
                0o444,
                0,
                reference,
                "batch",
            )
            .unwrap(),
        );
    }
    let ws = Workspace::open(&root, Arc::new(MemorySource { objects })).unwrap();
    ws.apply(Manifest {
        session_id: "batch".into(),
        generation: 1,
        entries,
    })
    .unwrap();
    let telemetry = ws.cache_telemetry().unwrap();
    assert_eq!(telemetry.fetched_objects, 64);
    assert_eq!(telemetry.resident_objects, 64);
    assert_eq!(telemetry.directory_syncs, 1);
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn legacy_metadata_migrates_without_scanning_the_object_tree() {
    let root = temp("catalog-migration");
    let ws = Workspace::open(&root, source(b"abc")).unwrap();
    ws.apply(manifest(1)).unwrap();
    drop(ws);

    let object = walk_files(&root.join("cache/objects"))
        .into_iter()
        .find(|path| {
            path.file_name()
                .unwrap()
                .to_string_lossy()
                .starts_with("sha256-")
        })
        .unwrap();
    let key = object
        .strip_prefix(root.join("cache/objects"))
        .unwrap()
        .to_string_lossy();
    for suffix in ["catalog.sqlite", "catalog.sqlite-wal", "catalog.sqlite-shm"] {
        let _ = std::fs::remove_file(root.join("cache").join(suffix));
    }
    std::fs::write(root.join("cache/metadata.v1"), format!("{key}\t3\t7\n")).unwrap();

    let ws = Workspace::open(&root, source(b"abc")).unwrap();
    assert_eq!(ws.cache_telemetry().unwrap().resident_objects, 1);
    assert!(!root.join("cache/metadata.v1").exists());
    assert!(root.join("cache/catalog.sqlite").is_file());
    std::fs::remove_dir_all(root).unwrap();
}
#[test]
fn invalid_bytes_never_become_visible() {
    let root = temp("invalid");
    let ws = Workspace::open(&root, source(b"abd")).unwrap();
    assert!(ws.apply(manifest(1)).is_err());
    assert!(ws.snapshot().by_path("sessions/one/raw.jsonl").is_none());
    std::fs::remove_dir_all(root).unwrap();
}
#[test]
fn stale_generation_is_ignored() {
    let root = temp("stale");
    let ws = Workspace::open(&root, source(b"abc")).unwrap();
    assert!(ws.apply(manifest(2)).unwrap().applied);
    assert!(!ws.apply(manifest(1)).unwrap().applied);
    std::fs::remove_dir_all(root).unwrap();
}

struct CountingSource {
    calls: Arc<AtomicUsize>,
}
impl ContentSource for CountingSource {
    fn fetch(&self, _: &ContentRef, w: &mut dyn Write) -> Result<(), FetchError> {
        self.calls.fetch_add(1, Ordering::SeqCst);
        std::thread::sleep(std::time::Duration::from_millis(20));
        w.write_all(b"abc")?;
        Ok(())
    }
}

#[test]
fn concurrent_sessions_coalesce_one_fetch() {
    let root = temp("coalesce");
    let calls = Arc::new(AtomicUsize::new(0));
    let ws = Workspace::open(
        &root,
        Arc::new(CountingSource {
            calls: calls.clone(),
        }),
    )
    .unwrap();
    let mut joins = vec![];
    for index in 0..8 {
        let ws = ws.clone();
        joins.push(std::thread::spawn(move || {
            ws.apply(Manifest {
                session_id: format!("s{index}"),
                generation: 1,
                entries: vec![
                    ManifestEntry::new(
                        format!("p{index}"),
                        "src",
                        "Session",
                        0o444,
                        0,
                        reference(),
                        "shared",
                    )
                    .unwrap(),
                ],
            })
            .unwrap();
        }));
    }
    for join in joins {
        join.join().unwrap();
    }
    assert_eq!(calls.load(Ordering::SeqCst), 1);
    std::fs::remove_dir_all(root).unwrap();
}

// A canonical-path collision is handled per entry, not by rejecting the whole
// update: the conflicting entry is skipped and recorded as `path_collision`, the
// resident file is never overwritten, and the rest of the update still applies.
// Failure prevented: one Session selecting a conflicting content at a path a
// second Session already holds would wipe out the whole update (and, worse,
// could silently overwrite the resident file).
#[test]
fn conflicting_session_content_is_skipped_per_entry_not_rejected() {
    let root = temp("conflict");
    let mut objects = BTreeMap::new();
    objects.insert(reference().digest, b"abc".to_vec());
    let second = ContentRef::new(
        "t",
        "sha256",
        "3608bca1e44ea6c4d268eb6db02260269892c0b42b86bbf1e77a6fa16c3c9282",
        3,
    )
    .unwrap();
    objects.insert(second.digest.clone(), b"xyz".to_vec());
    let ws = Workspace::open(&root, Arc::new(MemorySource { objects })).unwrap();
    ws.apply(Manifest {
        session_id: "a".into(),
        generation: 1,
        entries: vec![
            ManifestEntry::new("same", "a", "Session", 0o444, 0, reference(), "first").unwrap(),
        ],
    })
    .unwrap();
    let inode = ws.snapshot().by_path("same").unwrap().inode;
    // Session "b" selects the same path with different content plus a
    // non-conflicting path. The apply must succeed, not error.
    ws.apply(Manifest {
        session_id: "b".into(),
        generation: 1,
        entries: vec![
            ManifestEntry::new("same", "b", "Session", 0o444, 0, second, "second").unwrap(),
            ManifestEntry::new("other", "b", "Session", 0o444, 0, xyz_reference(), "kept").unwrap(),
        ],
    })
    .unwrap();
    // The resident file is untouched — "same" still reads "abc", not "xyz".
    assert_eq!(ws.open_inode(inode).unwrap().read(0, 3).unwrap(), b"abc");
    assert!(ws.snapshot().by_path("same").is_some());
    // The rest of the update applied: the non-conflicting path is visible.
    assert!(ws.snapshot().by_path("other").is_some());
    // The losing selector is recorded as `path_collision`; the winning selector
    // stays `available`; the path aggregate stays `available` (resident for one).
    let json_inode = ws.snapshot().by_path(".sageox/INDEX.json").unwrap().inode;
    let json = String::from_utf8(
        ws.open_inode(json_inode)
            .unwrap()
            .read(0, usize::MAX)
            .unwrap(),
    )
    .unwrap();
    assert!(json.contains("path_collision"));
    assert!(json.contains("\"session_id\":\"a\",\"reason\":\"first\",\"status\":\"available\""));
    assert!(
        json.contains("\"session_id\":\"b\",\"reason\":\"second\",\"status\":\"path_collision\"")
    );
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn collision_keeps_published_winner_when_lower_session_arrives() {
    let root = temp("reverse-conflict");
    let objects = BTreeMap::from([
        (reference().digest, b"abc".to_vec()),
        (xyz_reference().digest, b"xyz".to_vec()),
    ]);
    let ws = Workspace::open(&root, Arc::new(MemorySource { objects })).unwrap();
    ws.apply(Manifest {
        session_id: "b".into(),
        generation: 1,
        entries: vec![
            ManifestEntry::new("same", "b", "Session", 0o444, 0, xyz_reference(), "first").unwrap(),
        ],
    })
    .unwrap();
    let inode = ws.snapshot().by_path("same").unwrap().inode;
    ws.apply(Manifest {
        session_id: "a".into(),
        generation: 1,
        entries: vec![
            ManifestEntry::new("same", "a", "Session", 0o444, 0, reference(), "later").unwrap(),
        ],
    })
    .unwrap();
    assert_eq!(ws.open_inode(inode).unwrap().read(0, 3).unwrap(), b"xyz");
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn failed_replacement_keeps_published_snapshot_backing_resident() {
    let root = temp("failed-replacement-pins-live");
    let live_bytes = b"abc".to_vec();
    let spare_bytes = b"xyz".to_vec();
    let live = ContentRef::for_sha256("t", &live_bytes);
    let spare = ContentRef::for_sha256("t", &spare_bytes);
    let missing = ContentRef::for_sha256("t", b"CCCCCC");
    let ws = Workspace::open_with_config(
        &root,
        Arc::new(MemorySource {
            objects: BTreeMap::from([
                (live.digest.clone(), live_bytes.clone()),
                (spare.digest.clone(), spare_bytes),
            ]),
        }),
        oxfs::CacheConfig {
            max_bytes: 9,
            ..oxfs::CacheConfig::default()
        },
    )
    .unwrap();
    let selected = |session: &str, generation: u64, path: &str, content: ContentRef| Manifest {
        session_id: session.into(),
        generation,
        entries: vec![
            ManifestEntry::new(path, "src", "Session", 0o444, 0, content, "selected").unwrap(),
        ],
    };
    ws.apply(selected("live", 1, "live", live)).unwrap();
    ws.apply(selected("spare", 1, "spare", spare)).unwrap();
    ws.apply(Manifest {
        session_id: "spare".into(),
        generation: 2,
        entries: vec![],
    })
    .unwrap();
    let live_inode = ws.snapshot().by_path("live").unwrap().inode;

    assert!(
        ws.apply(selected("live", 2, "replacement", missing))
            .is_err()
    );
    assert!(ws.snapshot().by_path("live").is_some());
    assert_eq!(
        ws.open_inode(live_inode).unwrap().read(0, 99).unwrap(),
        live_bytes
    );
    std::fs::remove_dir_all(root).unwrap();
}

// The old namespace stays pinned until the swap, so a replacement transiently
// needs room for both selections. These three tests pin the whole class: a
// candidate that fits alone but not beside the live one must be REFUSED (never
// partially published), a candidate too big for the cache outright keeps the
// designed ranked-admission behavior, and a replacement that genuinely fits
// beside the live one must still land in full.
fn sized(session: &str, generation: u64, path: &str, content: ContentRef) -> Manifest {
    Manifest {
        session_id: session.into(),
        generation,
        entries: vec![
            ManifestEntry::new(path, "src", "Session", 0o444, 0, content, "selected").unwrap(),
        ],
    }
}

fn workspace_with(
    objects: &[(&ContentRef, &[u8])],
    max_bytes: u64,
) -> (std::path::PathBuf, Arc<Workspace>) {
    let root = temp("replacement-capacity");
    let ws = Workspace::open_with_config(
        &root,
        Arc::new(MemorySource {
            objects: objects
                .iter()
                .map(|(r, b)| (r.digest.clone(), b.to_vec()))
                .collect(),
        }),
        oxfs::CacheConfig {
            max_bytes,
            ..oxfs::CacheConfig::default()
        },
    )
    .unwrap();
    (root, ws)
}

#[test]
fn replacement_that_only_fits_without_the_live_selection_is_refused_not_truncated() {
    let live_bytes = vec![b'a'; 40];
    let next_bytes = vec![b'b'; 84];
    let live = ContentRef::for_sha256("t", &live_bytes);
    let next = ContentRef::for_sha256("t", &next_bytes);
    // 84 fits in 100 on its own, but not while the live 40 is pinned through the swap.
    let (root, ws) = workspace_with(&[(&live, &live_bytes), (&next, &next_bytes)], 100);

    ws.apply(sized("s", 1, "live", live)).unwrap();
    let live_inode = ws.snapshot().by_path("live").unwrap().inode;

    let error = ws.apply(sized("s", 2, "next", next)).unwrap_err();
    assert!(
        matches!(error, oxfs::WorkspaceError::ReplacementCapacity { .. }),
        "expected a loud replacement-capacity refusal, got {error:?}"
    );
    // The refusal must be total: the live selection is untouched and still readable,
    // and the replacement is nowhere to be seen.
    assert!(ws.snapshot().by_path("next").is_none());
    assert!(ws.snapshot().by_path("live").is_some());
    assert_eq!(
        ws.open_inode(live_inode).unwrap().read(0, 99).unwrap(),
        live_bytes
    );
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn selection_larger_than_the_whole_cache_still_ranks_and_stops() {
    let small_bytes = vec![b'a'; 30];
    let huge_bytes = vec![b'b'; 200];
    let small = ContentRef::for_sha256("t", &small_bytes);
    let huge = ContentRef::for_sha256("t", &huge_bytes);
    // 200 exceeds the 100 cache outright — that is an oversized selection, not a
    // replacement squeeze, and must keep the documented ranked-admission gate.
    let (root, ws) = workspace_with(&[(&small, &small_bytes), (&huge, &huge_bytes)], 100);

    let outcome = ws
        .apply(Manifest {
            session_id: "s".into(),
            generation: 1,
            entries: vec![
                ManifestEntry::new("small", "src", "Session", 0o444, 0, small, "sel").unwrap(),
                ManifestEntry::new("huge", "src", "Session", 0o444, 0, huge, "sel").unwrap(),
            ],
        })
        .expect("an oversized selection ranks and stops, it does not error");
    assert_eq!(outcome.available, 1);
    assert_eq!(outcome.stopped, 1);
    assert!(ws.snapshot().by_path("small").is_some());
    assert!(ws.snapshot().by_path("huge").is_none());
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn replacement_that_fits_beside_the_live_selection_still_applies_in_full() {
    let live_bytes = vec![b'a'; 40];
    let next_bytes = vec![b'b'; 50];
    let live = ContentRef::for_sha256("t", &live_bytes);
    let next = ContentRef::for_sha256("t", &next_bytes);
    // 40 + 50 = 90 <= 100: the swap has room for both, so nothing is refused.
    let (root, ws) = workspace_with(&[(&live, &live_bytes), (&next, &next_bytes)], 100);

    ws.apply(sized("s", 1, "live", live)).unwrap();
    let outcome = ws.apply(sized("s", 2, "next", next)).unwrap();
    assert_eq!(outcome.available, 1);
    assert_eq!(outcome.stopped, 0);
    assert!(ws.snapshot().by_path("next").is_some());
    assert!(ws.snapshot().by_path("live").is_none());
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn synthetic_index_is_resident_and_aggregates_selectors() {
    let root = temp("index");
    let ws = Workspace::open(&root, source(b"abc")).unwrap();
    for (session, reason) in [("one", "first reason"), ("two", "second reason")] {
        ws.apply(Manifest {
            session_id: session.into(),
            generation: 1,
            entries: vec![
                ManifestEntry::new("shared", "src", "Session", 0o444, 0, reference(), reason)
                    .unwrap(),
            ],
        })
        .unwrap();
    }
    let snapshot = ws.snapshot();
    let md = snapshot.by_path(".sageox/INDEX.md").unwrap().inode;
    let json = snapshot.by_path(".sageox/INDEX.json").unwrap().inode;
    let md = String::from_utf8(ws.open_inode(md).unwrap().read(0, usize::MAX).unwrap()).unwrap();
    let json =
        String::from_utf8(ws.open_inode(json).unwrap().read(0, usize::MAX).unwrap()).unwrap();
    for expected in ["shared", "one", "two", "first reason", "second reason"] {
        assert!(md.contains(expected), "markdown missing {expected}: {md}");
        assert!(json.contains(expected), "JSON missing {expected}: {json}");
    }
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn same_size_corruption_is_rejected_after_restart() {
    let root = temp("corrupt");
    let ws = Workspace::open(&root, source(b"abc")).unwrap();
    ws.apply(manifest(1)).unwrap();
    drop(ws);
    let object = walk_files(&root.join("cache/objects"))
        .into_iter()
        .find(|p| {
            p.file_name()
                .unwrap()
                .to_string_lossy()
                .starts_with("sha256-")
        })
        .unwrap();
    std::fs::write(&object, b"abd").unwrap();
    let ws = Workspace::open(&root, source(b"abc")).unwrap();
    ws.apply(manifest(1)).unwrap();
    let inode = ws
        .snapshot()
        .by_path("sessions/one/raw.jsonl")
        .unwrap()
        .inode;
    assert_eq!(ws.open_inode(inode).unwrap().read(0, 3).unwrap(), b"abc");
    std::fs::remove_dir_all(root).unwrap();
}
fn walk_files(root: &std::path::Path) -> Vec<std::path::PathBuf> {
    let mut out = vec![];
    for entry in std::fs::read_dir(root).unwrap() {
        let path = entry.unwrap().path();
        if path.is_dir() {
            out.extend(walk_files(&path))
        } else {
            out.push(path)
        }
    }
    out
}

fn xyz_reference() -> ContentRef {
    ContentRef::new(
        "t",
        "sha256",
        "3608bca1e44ea6c4d268eb6db02260269892c0b42b86bbf1e77a6fa16c3c9282",
        3,
    )
    .unwrap()
}

#[test]
fn ranked_admission_stops_but_keeps_later_resident_content() {
    let root = temp("ranked");
    let objects = BTreeMap::from([
        (reference().digest, b"abc".to_vec()),
        (xyz_reference().digest, b"xyz".to_vec()),
    ]);
    let ws = Workspace::open_with_config(
        &root,
        Arc::new(MemorySource { objects }),
        oxfs::CacheConfig {
            max_bytes: 3,
            ..oxfs::CacheConfig::default()
        },
    )
    .unwrap();
    ws.apply(Manifest {
        session_id: "old".into(),
        generation: 1,
        entries: vec![
            ManifestEntry::new(
                "resident",
                "x",
                "Session",
                0o444,
                0,
                xyz_reference(),
                "resident",
            )
            .unwrap(),
        ],
    })
    .unwrap();
    let outcome = ws
        .apply(Manifest {
            session_id: "new".into(),
            generation: 1,
            entries: vec![
                ManifestEntry::new("blocked", "a", "Session", 0o444, 0, reference(), "rank one")
                    .unwrap(),
                ManifestEntry::new(
                    "also-resident",
                    "b",
                    "Session",
                    0o444,
                    0,
                    xyz_reference(),
                    "rank two",
                )
                .unwrap(),
            ],
        })
        .unwrap();
    assert_eq!((outcome.available, outcome.stopped), (1, 2));
    assert!(ws.snapshot().by_path("blocked").is_some());
    assert!(ws.snapshot().by_path("resident").is_none());
    assert!(ws.snapshot().by_path("also-resident").is_none());
    let json_inode = ws.snapshot().by_path(".sageox/INDEX.json").unwrap().inode;
    let json = String::from_utf8(
        ws.open_inode(json_inode)
            .unwrap()
            .read(0, usize::MAX)
            .unwrap(),
    )
    .unwrap();
    assert!(
        json.contains("\"path\":\"also-resident\"") && json.contains("\"status\":\"no_space\"")
    );
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn dropped_content_is_evicted_and_oversize_never_fetches() {
    let root = temp("evict");
    let calls = Arc::new(AtomicUsize::new(0));
    let pressure = ContentRef::for_sha256("t", b"def");
    struct Source {
        calls: Arc<AtomicUsize>,
        pressure_digest: String,
    }
    impl ContentSource for Source {
        fn fetch(&self, r: &ContentRef, w: &mut dyn Write) -> Result<(), FetchError> {
            self.calls.fetch_add(1, Ordering::SeqCst);
            if r.digest == reference().digest {
                w.write_all(b"abc")?
            } else if r.digest == self.pressure_digest {
                w.write_all(b"def")?
            } else {
                w.write_all(b"xyz")?
            };
            Ok(())
        }
    }
    let ws = Workspace::open_with_config(
        &root,
        Arc::new(Source {
            calls: calls.clone(),
            pressure_digest: pressure.digest.clone(),
        }),
        // Replacement publication requires room for both the old and new
        // three-byte objects until the namespace swap is complete.
        oxfs::CacheConfig {
            max_bytes: 6,
            ..oxfs::CacheConfig::default()
        },
    )
    .unwrap();
    ws.apply(manifest(1)).unwrap();
    ws.apply(Manifest {
        session_id: "s1".into(),
        generation: 2,
        entries: vec![
            ManifestEntry::new(
                "replacement",
                "x",
                "Session",
                0o444,
                0,
                xyz_reference(),
                "new",
            )
            .unwrap(),
        ],
    })
    .unwrap();
    assert!(ws.snapshot().by_path("replacement").is_some());
    ws.apply(Manifest {
        session_id: "pressure".into(),
        generation: 1,
        entries: vec![
            ManifestEntry::new("pressure", "x", "Session", 0o444, 0, pressure, "pressure").unwrap(),
        ],
    })
    .unwrap();
    assert!(ws.snapshot().by_path("pressure").is_some());
    assert_eq!(ws.cache_telemetry().unwrap().resident_bytes, 6);
    assert_eq!(ws.eviction_counters().0, 1);

    let oversized = ContentRef::new("t", "sha256", "00", 7).unwrap();
    let before = calls.load(Ordering::SeqCst);
    let outcome = ws
        .apply(Manifest {
            session_id: "big".into(),
            generation: 1,
            entries: vec![
                ManifestEntry::new("too-big", "x", "Session", 0o444, 0, oversized, "big").unwrap(),
            ],
        })
        .unwrap();
    assert_eq!(calls.load(Ordering::SeqCst), before);
    assert_eq!(outcome.stopped, 1);
    std::fs::remove_dir_all(root).unwrap();
}
