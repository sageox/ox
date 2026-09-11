mod oxdir_remote;

use oxdir_remote::{RemoteClient, RemoteFetch, RemoteRegistry, resolve_selection};
use oxfs::mount;
use oxfs::nfs::{NfsServer, ServerConfig};
use oxfs::telemetry::{Telemetry, TelemetryConfig};
use oxfs::{
    CacheConfig, ContentRef, ContentSource, FetchError, Manifest, ManifestEntry, Workspace,
};
use std::collections::{BTreeMap, BTreeSet};
use std::fs::{self, File, Metadata};
use std::io::{self, BufRead, IsTerminal, Write};
use std::path::{Component, Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, RwLock};
use std::time::UNIX_EPOCH;

const DEFAULT_CACHE_BYTES: u64 = 10 * 1024 * 1024 * 1024;
const DEFAULT_SAGEOX_ENDPOINT: &str = "https://api.sageox.ai";

#[derive(Debug)]
struct Config {
    input: InputMode,
    mountpoint: PathBuf,
    state: Option<PathBuf>,
    cache_bytes: u64,
}

#[derive(Debug)]
enum InputMode {
    Local(PathBuf),
    Remote(String),
}

#[derive(Clone, Debug, Eq, PartialEq)]
struct SourceFile {
    path: PathBuf,
    len: u64,
    modified: Option<std::time::SystemTime>,
}

#[derive(Clone, Debug)]
enum SourceObject {
    Local(SourceFile),
    Remote(RemoteFetch),
}

#[derive(Default)]
struct MaterialSource {
    files: RwLock<BTreeMap<String, SourceObject>>,
}

impl MaterialSource {
    fn replace(
        &self,
        files: BTreeMap<String, SourceObject>,
    ) -> io::Result<BTreeMap<String, SourceObject>> {
        let mut current = self
            .files
            .write()
            .map_err(|_| io::Error::other("material source lock poisoned"))?;
        Ok(std::mem::replace(&mut *current, files))
    }
}

impl ContentSource for MaterialSource {
    fn fetch(&self, reference: &ContentRef, output: &mut dyn Write) -> Result<(), FetchError> {
        let object = self
            .files
            .read()
            .map_err(|_| io::Error::other("material source lock poisoned"))?
            .get(&source_key(reference))
            .cloned()
            .ok_or(FetchError::NotFound)?;
        match object {
            SourceObject::Local(expected) => {
                let before = fs::metadata(&expected.path)?;
                verify_metadata(&expected, &before)?;
                let mut input = File::open(&expected.path)?;
                io::copy(&mut input, output)?;
                let after = input.metadata()?;
                verify_metadata(&expected, &after)?;
                Ok(())
            }
            SourceObject::Remote(remote) => remote.fetch(output).map_err(FetchError::Io),
        }
    }
}

fn source_key(reference: &ContentRef) -> String {
    format!("{}\0{}", reference.tenant, reference.storage_key())
}

fn verify_metadata(expected: &SourceFile, actual: &Metadata) -> io::Result<()> {
    if !actual.is_file()
        || actual.len() != expected.len
        || actual.modified().ok() != expected.modified
    {
        return Err(io::Error::other(format!(
            "source changed while selecting: {}",
            expected.path.display()
        )));
    }
    Ok(())
}

fn main() {
    std::process::exit(run_main());
}

fn run_main() -> i32 {
    let config = match parse_args(std::env::args().skip(1).collect()) {
        Ok(config) => config,
        Err(error) => {
            eprintln!("oxdirtest: {error}");
            return 2;
        }
    };
    let telemetry_config = TelemetryConfig::new(
        std::env::var("SAGEOX_ENDPOINT").unwrap_or_else(|_| DEFAULT_SAGEOX_ENDPOINT.into()),
        std::env::var("SAGEOX_TOKEN").unwrap_or_default(),
    );
    let telemetry = match Telemetry::init(telemetry_config) {
        Ok(telemetry) => telemetry,
        Err(error) => {
            eprintln!("oxdirtest: warning: SageOx tracing disabled: {error}");
            Telemetry::disabled()
        }
    };
    let root = telemetry.session_span("oxdirtest", config.cache_bytes);
    let _entered = root.enter();
    if let Err(error) = run(config) {
        root.record("otel.status_code", "ERROR");
        root.record("error.message", tracing::field::display(&error));
        eprintln!("oxdirtest: {error}");
        return 1;
    }
    0
}

fn run(config: Config) -> Result<(), Box<dyn std::error::Error>> {
    if !cfg!(target_os = "macos") {
        return Err("oxdirtest requires macOS mount_nfs".into());
    }
    let temporary = config.state.is_none();
    let state = config.state.unwrap_or_else(temp_root);
    fs::create_dir_all(&state)?;
    let result = match config.input {
        InputMode::Local(source) => {
            let source_root = source.canonicalize()?;
            if !source_root.is_dir() {
                return Err(format!("source is not a directory: {}", source_root.display()).into());
            }
            run_local_session(&config.mountpoint, &source_root, &state, config.cache_bytes)
        }
        InputMode::Remote(host) => {
            run_remote_session(&config.mountpoint, &host, &state, config.cache_bytes)
        }
    };
    if temporary
        && let Err(error) = fs::remove_dir_all(&state)
        && error.kind() != io::ErrorKind::NotFound
    {
        eprintln!(
            "oxdirtest: warning: cannot remove {}: {error}",
            state.display()
        );
    }
    result
}

fn run_local_session(
    mountpoint: &Path,
    source_root: &Path,
    state: &Path,
    cache_bytes: u64,
) -> Result<(), Box<dyn std::error::Error>> {
    let source = Arc::new(MaterialSource::default());
    let workspace = Workspace::open_with_config(
        &state.join("workspace"),
        source.clone(),
        CacheConfig {
            max_bytes: cache_bytes,
            ..CacheConfig::default()
        },
    )?;
    let stop = Arc::new(AtomicBool::new(false));
    install_stop_handler(stop.clone())?;
    let server = NfsServer::new(workspace.clone(), ServerConfig::default()).spawn()?;
    let mounted = mount::mount_server(server.address().port(), mountpoint)?;
    eprintln!(
        "oxdirtest: mounted {} from {}\nstate: {}\naccess log: {}\ncommands: select DIR [DIR ...] | clear | status | metrics | help | quit",
        mountpoint.display(),
        source_root.display(),
        state.display(),
        state.join("workspace/state/access.jsonl").display(),
    );
    let interaction = command_loop(source_root, mountpoint, &source, &workspace, &stop);
    eprintln!("oxdirtest: unmounting {}...", mountpoint.display());
    let teardown = mounted
        .unmount()
        .and_then(|()| server.shutdown())
        .map_err(Box::<dyn std::error::Error>::from);
    drop(workspace);
    teardown?;
    interaction
}

fn run_remote_session(
    mountpoint: &Path,
    host: &str,
    state: &Path,
    cache_bytes: u64,
) -> Result<(), Box<dyn std::error::Error>> {
    let registry = RemoteRegistry::discover(host)?;
    if registry.repos().next().is_none() {
        return Err(format!("no SageOx repositories registered for {host}").into());
    }
    let client = RemoteClient::for_host(host)?;
    let source = Arc::new(MaterialSource::default());
    let workspace = Workspace::open_with_config(
        &state.join("workspace"),
        source.clone(),
        CacheConfig {
            max_bytes: cache_bytes,
            ..CacheConfig::default()
        },
    )?;
    let stop = Arc::new(AtomicBool::new(false));
    install_stop_handler(stop.clone())?;
    let server = NfsServer::new(workspace.clone(), ServerConfig::default()).spawn()?;
    let mounted = mount::mount_server(server.address().port(), mountpoint)?;
    eprintln!(
        "oxdirtest: mounted {} from remote host {}\nstate: {}\naccess log: {}\ncommands: repos | select PATH [PATH ...] | clear | status | metrics | help | quit",
        mountpoint.display(),
        registry.host(),
        state.display(),
        state.join("workspace/state/access.jsonl").display(),
    );
    let interaction = remote_command_loop(
        mountpoint,
        state,
        cache_bytes,
        &registry,
        &client,
        &source,
        &workspace,
        &stop,
    );
    eprintln!("oxdirtest: unmounting {}...", mountpoint.display());
    let teardown = mounted
        .unmount()
        .and_then(|()| server.shutdown())
        .map_err(Box::<dyn std::error::Error>::from);
    drop(workspace);
    teardown?;
    interaction
}

#[allow(clippy::too_many_arguments)]
fn remote_command_loop(
    mountpoint: &Path,
    state: &Path,
    cache_bytes: u64,
    registry: &RemoteRegistry,
    client: &Arc<RemoteClient>,
    source: &MaterialSource,
    workspace: &Workspace,
    stop: &AtomicBool,
) -> Result<(), Box<dyn std::error::Error>> {
    let stdin = io::stdin();
    let interactive = stdin.is_terminal();
    let mut generation = 0u64;
    let mut selected = BTreeSet::new();
    while !stop.load(Ordering::SeqCst) {
        if interactive {
            eprint!("oxdirtest> ");
            io::stderr().flush()?;
        }
        let mut line = String::new();
        if stdin.lock().read_line(&mut line)? == 0 {
            break;
        }
        let words: Vec<_> = line.split_whitespace().collect();
        let Some(command) = words.first().copied() else {
            continue;
        };
        match command {
            "repos" => {
                for repo in registry.repos() {
                    eprintln!("{}  {}", repo.virtual_root, repo.origin);
                }
            }
            "select" if words.len() > 1 => {
                let mut candidate = selected.clone();
                candidate.extend(words[1..].iter().map(|value| (*value).to_owned()));
                if candidate == selected {
                    eprintln!("oxdirtest: paths already selected; selection unchanged");
                    continue;
                }
                if publish_remote_selection(
                    mountpoint,
                    state,
                    cache_bytes,
                    registry,
                    client,
                    source,
                    workspace,
                    &mut generation,
                    &candidate,
                )? {
                    selected = candidate;
                }
            }
            "select" => eprintln!("usage: select PATH [PATH ...]"),
            "clear" => {
                let candidate = BTreeSet::new();
                if publish_remote_selection(
                    mountpoint,
                    state,
                    cache_bytes,
                    registry,
                    client,
                    source,
                    workspace,
                    &mut generation,
                    &candidate,
                )? {
                    selected = candidate;
                }
            }
            "status" => eprintln!(
                "oxdirtest: generation={} selections={} [{}]",
                generation,
                selected.len(),
                selected.iter().cloned().collect::<Vec<_>>().join(", ")
            ),
            "metrics" => print_metrics(workspace)?,
            "help" => eprintln!(
                "commands:\n  repos                 list discovered SageOx remotes\n  select PATH [PATH ...] add remote files or directories to the mount\n  clear                 reset the mount to no selected paths\n  status                show current selection\n  metrics               show cumulative cache and durability metrics\n  quit                  unmount and exit"
            ),
            "quit" | "exit" => break,
            other => eprintln!("oxdirtest: unknown command {other:?}; type help"),
        }
    }
    Ok(())
}

#[allow(clippy::too_many_arguments)]
fn publish_remote_selection(
    mountpoint: &Path,
    state: &Path,
    cache_bytes: u64,
    registry: &RemoteRegistry,
    client: &Arc<RemoteClient>,
    source: &MaterialSource,
    workspace: &Workspace,
    generation: &mut u64,
    selected: &BTreeSet<String>,
) -> Result<bool, Box<dyn std::error::Error>> {
    eprintln!(
        "oxdirtest: resolving {} remote selections...",
        selected.len()
    );
    let staging = state.join("remote-staging-next");
    match fs::remove_dir_all(&staging) {
        Ok(()) => {}
        Err(error) if error.kind() == io::ErrorKind::NotFound => {}
        Err(error) => return Err(error.into()),
    }
    fs::create_dir_all(&staging)?;

    let indexed = match index_remote_files(registry, client, selected, &staging, cache_bytes) {
        Ok(indexed) => indexed,
        Err(error) => {
            let _ = fs::remove_dir_all(&staging);
            eprintln!("oxdirtest: remote selection rejected: {error}");
            eprintln!("oxdirtest: selection unchanged");
            return Ok(false);
        }
    };
    let next_generation = generation.saturating_add(1);
    let previous_sources = source.replace(indexed.apply_sources)?;
    let outcome = match workspace.apply(Manifest {
        session_id: "remote-selection".into(),
        generation: next_generation,
        entries: indexed.entries,
    }) {
        Ok(outcome) => outcome,
        Err(error) => {
            source.replace(previous_sources)?;
            let _ = fs::remove_dir_all(&staging);
            eprintln!("oxdirtest: remote selection rejected: {error}");
            eprintln!("oxdirtest: selection unchanged; remove paths or raise --cache-bytes");
            return Ok(false);
        }
    };
    source.replace(indexed.retained_sources)?;
    fs::remove_dir_all(&staging)?;
    *generation = next_generation;
    eprintln!(
        "oxdirtest: generation={} selections={} files={} bytes={} available={} stopped={} mountpoint={}",
        generation,
        selected.len(),
        indexed.files,
        indexed.bytes,
        outcome.available,
        outcome.stopped,
        mountpoint.display()
    );
    if outcome.stopped > 0 {
        eprintln!(
            "oxdirtest: WARNING {} of {} file(s) did not fit the cache and are NOT in the mount",
            outcome.stopped, indexed.files
        );
    }
    Ok(true)
}

struct RemoteIndexed {
    entries: Vec<ManifestEntry>,
    apply_sources: BTreeMap<String, SourceObject>,
    retained_sources: BTreeMap<String, SourceObject>,
    files: usize,
    bytes: u64,
}

fn index_remote_files(
    registry: &RemoteRegistry,
    client: &Arc<RemoteClient>,
    selected: &BTreeSet<String>,
    staging: &Path,
    cache_bytes: u64,
) -> io::Result<RemoteIndexed> {
    let mut files = BTreeMap::new();
    for selection in selected {
        for file in resolve_selection(registry, client, selection, cache_bytes)? {
            files.insert(file.virtual_path.clone(), file);
        }
    }
    let mut entries = Vec::with_capacity(files.len());
    let mut apply_sources = BTreeMap::new();
    let mut retained_sources = BTreeMap::new();
    let mut total_bytes = 0u64;
    for (index, (virtual_path, file)) in files.into_iter().enumerate() {
        let tenant = format!("{}/{}", registry.host(), file.repo.virtual_root);
        let (content, apply_source, retained_source) = if let Some(pointer) = file.pointer {
            let content = ContentRef::new(&tenant, "sha256", &pointer.oid, pointer.size)
                .map_err(io::Error::other)?;
            let remote = SourceObject::Remote(RemoteFetch::Lfs {
                client: client.clone(),
                repo: file.repo.clone(),
                pointer,
            });
            (content, remote.clone(), remote)
        } else {
            let content = ContentRef::for_sha256(&tenant, &file.bytes);
            let staged = staging.join(format!("{index}.object"));
            fs::write(&staged, &file.bytes)?;
            let metadata = fs::metadata(&staged)?;
            let apply = SourceObject::Local(SourceFile {
                path: staged,
                len: metadata.len(),
                modified: metadata.modified().ok(),
            });
            let retained = SourceObject::Remote(RemoteFetch::Raw {
                client: client.clone(),
                repo: file.repo.clone(),
                path: file.repo_path.clone(),
            });
            (content, apply, retained)
        };
        total_bytes = total_bytes
            .checked_add(content.size)
            .ok_or_else(|| io::Error::other("remote selection size overflow"))?;
        if total_bytes > cache_bytes {
            return Err(io::Error::other("remote selection exceeds cache capacity"));
        }
        let key = source_key(&content);
        apply_sources.insert(key.clone(), apply_source);
        retained_sources.insert(key, retained_source);
        entries.push(
            ManifestEntry::new(
                virtual_path,
                file.repo.origin.as_str(),
                "SageOxRemote",
                file.mode,
                file.mtime_secs,
                content,
                "human-selected SageOx remote path",
            )
            .map_err(io::Error::other)?,
        );
    }
    Ok(RemoteIndexed {
        files: entries.len(),
        entries,
        apply_sources,
        retained_sources,
        bytes: total_bytes,
    })
}

fn command_loop(
    source_root: &Path,
    mountpoint: &Path,
    source: &MaterialSource,
    workspace: &Workspace,
    stop: &AtomicBool,
) -> Result<(), Box<dyn std::error::Error>> {
    let stdin = io::stdin();
    let interactive = stdin.is_terminal();
    let mut generation = 0u64;
    let mut selected = BTreeSet::new();
    while !stop.load(Ordering::SeqCst) {
        if interactive {
            eprint!("oxdirtest> ");
            io::stderr().flush()?;
        }
        let mut line = String::new();
        if stdin.lock().read_line(&mut line)? == 0 {
            break;
        }
        let words: Vec<_> = line.split_whitespace().collect();
        let Some(command) = words.first().copied() else {
            continue;
        };
        match command {
            "select" if words.len() > 1 => {
                let mut candidate = selected.clone();
                candidate.extend(parse_selections(source_root, &words[1..])?);
                if candidate == selected {
                    eprintln!("oxdirtest: directories already selected; selection unchanged");
                    continue;
                }
                if publish_selection(
                    source_root,
                    mountpoint,
                    source,
                    workspace,
                    &mut generation,
                    &candidate,
                )? {
                    selected = candidate;
                }
            }
            "select" => eprintln!("usage: select DIR [DIR ...]"),
            "clear" => {
                let candidate = BTreeSet::new();
                if publish_selection(
                    source_root,
                    mountpoint,
                    source,
                    workspace,
                    &mut generation,
                    &candidate,
                )? {
                    selected = candidate;
                }
            }
            "status" => eprintln!(
                "oxdirtest: generation={} selections={} [{}]",
                generation,
                selected.len(),
                selected
                    .iter()
                    .map(|path| path.display().to_string())
                    .collect::<Vec<_>>()
                    .join(", ")
            ),
            "metrics" => print_metrics(workspace)?,
            "help" => eprintln!(
                "commands:\n  select DIR [DIR ...]  add backing directories to the mount\n  clear                 reset the mount to no selected directories\n  status                show current selection\n  metrics               show cumulative cache and durability metrics\n  quit                  unmount and exit"
            ),
            "quit" | "exit" => break,
            other => eprintln!("oxdirtest: unknown command {other:?}; type help"),
        }
    }
    Ok(())
}

fn publish_selection(
    source_root: &Path,
    mountpoint: &Path,
    source: &MaterialSource,
    workspace: &Workspace,
    generation: &mut u64,
    selected: &BTreeSet<PathBuf>,
) -> Result<bool, Box<dyn std::error::Error>> {
    let paths: Vec<_> = selected.iter().cloned().collect();
    eprintln!(
        "oxdirtest: scanning {} selected directories...",
        paths.len()
    );
    let indexed = index_files(source_root, &paths)?;
    let next_generation = generation.saturating_add(1);
    let previous_sources = source.replace(indexed.sources)?;
    let outcome = match workspace.apply(Manifest {
        session_id: "human-selection".into(),
        generation: next_generation,
        entries: indexed.entries,
    }) {
        Ok(outcome) => outcome,
        Err(error) => {
            source.replace(previous_sources)?;
            eprintln!("oxdirtest: selection rejected: {error}");
            eprintln!("oxdirtest: selection unchanged; remove directories or raise --cache-bytes");
            return Ok(false);
        }
    };
    *generation = next_generation;
    eprintln!(
        "oxdirtest: generation={} directories={} files={} bytes={} available={} stopped={} mountpoint={}",
        generation,
        selected.len(),
        indexed.files,
        indexed.bytes,
        outcome.available,
        outcome.stopped,
        mountpoint.display()
    );
    if outcome.stopped > 0 {
        eprintln!(
            "oxdirtest: WARNING {} of {} file(s) did not fit the cache and are NOT in the mount",
            outcome.stopped, indexed.files
        );
    }
    Ok(true)
}

fn print_metrics(workspace: &Workspace) -> io::Result<()> {
    let telemetry = workspace.cache_telemetry()?;
    let nfs = workspace.nfs_telemetry();
    let (capacity, remaining) = workspace.cache_capacity();
    eprintln!(
        concat!(
            "level=INFO action=metrics ",
            "nfs_lookup={} nfs_getattr={} nfs_access={} nfs_readdir={} nfs_read={} ",
            "catalog_lookups={} resident_hits={} resident_misses={} ",
            "opened_objects={} opened_bytes={} ",
            "fetched_objects={} fetched_bytes={} refetched_objects={} refetched_bytes={} ",
            "evicted_objects={} evicted_bytes={} evict_blocked={} ",
            "catalog_transactions={} catalog_commit_us={} object_sync_us={} ",
            "directory_syncs={} directory_sync_us={} ",
            "resident_objects={} resident_bytes={} pending_objects={} ",
            "catalog_bytes={} wal_bytes={} cache_capacity={} cache_remaining={}"
        ),
        nfs.lookup,
        nfs.getattr,
        nfs.access,
        nfs.readdir,
        nfs.read,
        telemetry.catalog_lookups,
        telemetry.resident_hits,
        telemetry.resident_misses,
        telemetry.opened_objects,
        telemetry.opened_bytes,
        telemetry.fetched_objects,
        telemetry.fetched_bytes,
        telemetry.refetched_objects,
        telemetry.refetched_bytes,
        telemetry.evicted_objects,
        telemetry.evicted_bytes,
        telemetry.evict_blocked,
        telemetry.catalog_transactions,
        telemetry.catalog_commit_us,
        telemetry.object_sync_us,
        telemetry.directory_syncs,
        telemetry.directory_sync_us,
        telemetry.resident_objects,
        telemetry.resident_bytes,
        telemetry.pending_objects,
        telemetry.catalog_bytes,
        telemetry.wal_bytes,
        capacity,
        remaining,
    );
    Ok(())
}

struct Indexed {
    entries: Vec<ManifestEntry>,
    sources: BTreeMap<String, SourceObject>,
    files: usize,
    bytes: u64,
}

fn index_files(source_root: &Path, selections: &[PathBuf]) -> io::Result<Indexed> {
    let mut files = BTreeMap::new();
    for selection in selections {
        collect_files(&source_root.join(selection), &mut files)?;
    }
    let mut entries = Vec::with_capacity(files.len());
    let mut sources = BTreeMap::new();
    for (absolute, metadata) in files {
        let relative = absolute
            .strip_prefix(source_root)
            .map_err(io::Error::other)?;
        let content = hash_file(&absolute, &metadata)?;
        let source_file = SourceFile {
            path: absolute.clone(),
            len: metadata.len(),
            modified: metadata.modified().ok(),
        };
        sources.insert(source_key(&content), SourceObject::Local(source_file));
        entries.push(
            ManifestEntry::new(
                path_string(relative)?,
                absolute.display().to_string(),
                "LocalDirectory",
                readonly_mode(&metadata),
                modified_secs(&metadata),
                content,
                "human-selected local directory",
            )
            .map_err(io::Error::other)?,
        );
    }
    let total_bytes = entries.iter().map(|entry| entry.content.size).sum();
    Ok(Indexed {
        files: entries.len(),
        entries,
        sources,
        bytes: total_bytes,
    })
}

fn collect_files(path: &Path, files: &mut BTreeMap<PathBuf, Metadata>) -> io::Result<()> {
    let metadata = fs::symlink_metadata(path)?;
    if metadata.file_type().is_symlink() {
        eprintln!("oxdirtest: skipping symlink {}", path.display());
        return Ok(());
    }
    if metadata.is_file() {
        files.insert(path.to_owned(), metadata);
        return Ok(());
    }
    if !metadata.is_dir() {
        eprintln!("oxdirtest: skipping special file {}", path.display());
        return Ok(());
    }
    let mut children = fs::read_dir(path)?.collect::<Result<Vec<_>, _>>()?;
    children.sort_by_key(|entry| entry.file_name());
    for child in children {
        collect_files(&child.path(), files)?;
    }
    Ok(())
}

fn hash_file(path: &Path, metadata: &Metadata) -> io::Result<ContentRef> {
    let expected = SourceFile {
        path: path.to_owned(),
        len: metadata.len(),
        modified: metadata.modified().ok(),
    };
    let file = File::open(path)?;
    let content = ContentRef::for_sha256_reader("oxdirtest", &file)?;
    verify_metadata(&expected, &file.metadata()?)?;
    if content.size != expected.len {
        return Err(io::Error::other(format!(
            "source changed while selecting: {}",
            path.display()
        )));
    }
    Ok(content)
}

fn parse_selections(source_root: &Path, values: &[&str]) -> io::Result<Vec<PathBuf>> {
    let mut unique = BTreeSet::new();
    for value in values {
        let relative = safe_relative(Path::new(value))?;
        let canonical = source_root.join(&relative).canonicalize()?;
        if !canonical.starts_with(source_root) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                format!("selection escapes source root: {value}"),
            ));
        }
        let canonical_relative = canonical
            .strip_prefix(source_root)
            .map_err(io::Error::other)?;
        unique.insert(canonical_relative.to_owned());
    }
    Ok(unique.into_iter().collect())
}

fn safe_relative(path: &Path) -> io::Result<PathBuf> {
    if path.as_os_str().is_empty()
        || path
            .components()
            .any(|part| !matches!(part, Component::Normal(_) | Component::CurDir))
    {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            format!(
                "selection must be a relative path inside the source: {}",
                path.display()
            ),
        ));
    }
    Ok(path.to_owned())
}

fn path_string(path: &Path) -> io::Result<String> {
    path.to_str().map(str::to_owned).ok_or_else(|| {
        io::Error::new(
            io::ErrorKind::InvalidData,
            format!("path is not UTF-8: {}", path.display()),
        )
    })
}

#[cfg(unix)]
fn readonly_mode(metadata: &Metadata) -> u32 {
    use std::os::unix::fs::MetadataExt;
    metadata.mode() & 0o555
}

#[cfg(not(unix))]
fn readonly_mode(_: &Metadata) -> u32 {
    0o444
}

fn modified_secs(metadata: &Metadata) -> u64 {
    metadata
        .modified()
        .ok()
        .and_then(|time| time.duration_since(UNIX_EPOCH).ok())
        .map_or(0, |duration| duration.as_secs())
}

fn temp_root() -> PathBuf {
    std::env::temp_dir().join(format!("oxdirtest-{}", std::process::id()))
}

#[cfg(unix)]
fn install_stop_handler(stop: Arc<AtomicBool>) -> Result<(), Box<dyn std::error::Error>> {
    ctrlc::set_handler(move || stop.store(true, Ordering::SeqCst))?;
    Ok(())
}

#[cfg(not(unix))]
fn install_stop_handler(_: Arc<AtomicBool>) -> Result<(), Box<dyn std::error::Error>> {
    Err("signal handling is only available on unix".into())
}

fn parse_args(args: Vec<String>) -> Result<Config, String> {
    let mut source = None;
    let mut git_host = None;
    let mut mountpoint = None;
    let mut state = None;
    let mut cache_bytes = DEFAULT_CACHE_BYTES;
    let mut index = 0;
    while index < args.len() {
        let option = &args[index];
        index += 1;
        let value = args
            .get(index)
            .ok_or_else(|| format!("missing value for {option}"))?;
        index += 1;
        match option.as_str() {
            "--source" => source = Some(PathBuf::from(value)),
            "--git-host" => git_host = Some(value.to_owned()),
            "--mountpoint" => mountpoint = Some(PathBuf::from(value)),
            "--state" => state = Some(PathBuf::from(value)),
            "--cache-bytes" => {
                cache_bytes = value
                    .parse()
                    .ok()
                    .filter(|size| *size > 0)
                    .ok_or_else(|| "--cache-bytes must be a positive integer".to_string())?
            }
            _ => return Err(format!("unknown option: {option}")),
        }
    }
    let input = match (source, git_host) {
        (Some(source), None) => InputMode::Local(source),
        (None, Some(host)) => InputMode::Remote(host),
        (None, None) => return Err(usage()),
        (Some(_), Some(_)) => {
            return Err("--source and --git-host are mutually exclusive".into());
        }
    };
    Ok(Config {
        input,
        mountpoint: mountpoint.ok_or_else(usage)?,
        state,
        cache_bytes,
    })
}

fn usage() -> String {
    "usage: oxdirtest (--source DIR | --git-host HOST) --mountpoint DIR [--state DIR] [--cache-bytes N]".into()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fixture() -> PathBuf {
        let root = std::env::temp_dir().join(format!(
            "oxdirtest-test-{}-{:?}",
            std::process::id(),
            std::thread::current().id()
        ));
        let _ = fs::remove_dir_all(&root);
        fs::create_dir_all(root.join("one/nested")).unwrap();
        fs::create_dir_all(root.join("two")).unwrap();
        fs::write(root.join("one/a.txt"), b"a").unwrap();
        fs::write(root.join("one/nested/b.txt"), b"bee").unwrap();
        fs::write(root.join("two/c.txt"), b"see").unwrap();
        root
    }

    #[test]
    fn parses_mutually_exclusive_input_modes() {
        let local = parse_args(vec![
            "--source".into(),
            "/tmp/source".into(),
            "--mountpoint".into(),
            "/tmp/mount".into(),
        ])
        .unwrap();
        assert!(matches!(local.input, InputMode::Local(_)));

        let remote = parse_args(vec![
            "--git-host".into(),
            "git.test.sageox.ai".into(),
            "--mountpoint".into(),
            "/tmp/mount".into(),
        ])
        .unwrap();
        assert!(matches!(remote.input, InputMode::Remote(_)));

        assert!(
            parse_args(vec![
                "--source".into(),
                "/tmp/source".into(),
                "--git-host".into(),
                "git.test.sageox.ai".into(),
                "--mountpoint".into(),
                "/tmp/mount".into(),
            ])
            .is_err()
        );
    }

    #[test]
    fn indexes_only_selected_directories() {
        let root = fixture();
        let indexed = index_files(&root, &[PathBuf::from("one")]).unwrap();
        let paths: Vec<_> = indexed
            .entries
            .iter()
            .map(|entry| entry.path.as_str())
            .collect();
        assert_eq!(paths, ["one/a.txt", "one/nested/b.txt"]);
        assert_eq!(indexed.files, 2);
        assert_eq!(indexed.bytes, 4);
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn rejects_parent_and_absolute_selections() {
        assert!(safe_relative(Path::new("../secret")).is_err());
        assert!(safe_relative(Path::new("/tmp/secret")).is_err());
        assert!(safe_relative(Path::new("ok/nested")).is_ok());
    }

    #[test]
    fn local_source_detects_changed_files() {
        let root = fixture();
        let path = root.join("one/a.txt");
        let metadata = fs::metadata(&path).unwrap();
        let expected = SourceFile {
            path: path.clone(),
            len: metadata.len(),
            modified: metadata.modified().ok(),
        };
        fs::write(&path, b"changed length").unwrap();
        assert!(verify_metadata(&expected, &fs::metadata(path).unwrap()).is_err());
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn selected_files_flow_through_workspace_and_clear() {
        let root = fixture();
        let indexed = index_files(&root, &[PathBuf::from("one")]).unwrap();
        let source = Arc::new(MaterialSource::default());
        source.replace(indexed.sources).unwrap();
        let workspace = Workspace::open_with_config(
            &root.join("state"),
            source,
            CacheConfig {
                max_bytes: 1024 * 1024,
                ..CacheConfig::default()
            },
        )
        .unwrap();
        workspace
            .apply(Manifest {
                session_id: "human-selection".into(),
                generation: 1,
                entries: indexed.entries,
            })
            .unwrap();
        let inode = workspace
            .snapshot()
            .by_path("one/nested/b.txt")
            .unwrap()
            .inode;
        assert_eq!(
            workspace.open_inode(inode).unwrap().read(0, 16).unwrap(),
            b"bee"
        );

        workspace
            .apply(Manifest {
                session_id: "human-selection".into(),
                generation: 2,
                entries: Vec::new(),
            })
            .unwrap();
        assert!(workspace.snapshot().by_path("one/nested/b.txt").is_none());
        drop(workspace);
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn publishing_an_additional_directory_preserves_the_existing_selection() {
        let root = fixture();
        let source = Arc::new(MaterialSource::default());
        let workspace = Workspace::open_with_config(
            &root.join("state"),
            source.clone(),
            CacheConfig {
                max_bytes: 1024 * 1024,
                ..CacheConfig::default()
            },
        )
        .unwrap();
        let mut generation = 0;
        let mut selected = BTreeSet::from([PathBuf::from("one")]);

        assert!(
            publish_selection(
                &root,
                &root.join("mount"),
                &source,
                &workspace,
                &mut generation,
                &selected,
            )
            .unwrap()
        );
        selected.insert(PathBuf::from("two"));
        assert!(
            publish_selection(
                &root,
                &root.join("mount"),
                &source,
                &workspace,
                &mut generation,
                &selected,
            )
            .unwrap()
        );

        let snapshot = workspace.snapshot();
        assert!(snapshot.by_path("one/a.txt").is_some());
        assert!(snapshot.by_path("one/nested/b.txt").is_some());
        assert!(snapshot.by_path("two/c.txt").is_some());
        assert_eq!(generation, 2);
        drop(snapshot);
        drop(workspace);
        fs::remove_dir_all(root).unwrap();
    }
}
