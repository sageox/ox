use curl::easy::{Auth, Easy, List, WriteError};
use flate2::read::GzDecoder;
use serde::{Deserialize, Serialize};
use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::io::{self, Read, Write};
use std::path::{Component, Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::Arc;
use tar::Archive;
use url::Url;

const LFS_POINTER_VERSION: &str = "version https://git-lfs.github.com/spec/v1";
const MAX_POINTER_BYTES: usize = 200;

#[derive(Clone, Debug)]
pub struct RemoteRepo {
    pub virtual_root: String,
    pub origin: Url,
}

#[derive(Clone, Debug)]
pub struct RemoteRegistry {
    host: String,
    repos: BTreeMap<String, RemoteRepo>,
}

impl RemoteRegistry {
    pub fn discover(host: &str) -> io::Result<Self> {
        let endpoint = endpoint_for_git_host(host)?;
        let data = sageox_data_root()?.join(endpoint);
        Self::discover_at(host, &data)
    }

    pub fn discover_at(host: &str, endpoint_root: &Path) -> io::Result<Self> {
        endpoint_for_git_host(host)?;
        let mut repos = BTreeMap::new();
        for category in ["ledgers", "teams", "kb"] {
            let category_root = endpoint_root.join(category);
            let entries = match fs::read_dir(&category_root) {
                Ok(entries) => entries,
                Err(error) if error.kind() == io::ErrorKind::NotFound => continue,
                Err(error) => return Err(error),
            };
            for entry in entries {
                let entry = entry?;
                if !entry.file_type()?.is_dir() {
                    continue;
                }
                let id = entry.file_name().into_string().map_err(|_| {
                    io::Error::new(io::ErrorKind::InvalidData, "repository id is not UTF-8")
                })?;
                let Some(origin) = read_origin(&entry.path().join(".git/config"))? else {
                    continue;
                };
                let origin = match sanitize_origin(&origin, host) {
                    Ok(origin) => origin,
                    Err(_) => continue,
                };
                let virtual_root = format!("{category}/{id}");
                repos.insert(
                    virtual_root.clone(),
                    RemoteRepo {
                        virtual_root,
                        origin,
                    },
                );
            }
        }
        Ok(Self {
            host: host.to_owned(),
            repos,
        })
    }

    pub fn host(&self) -> &str {
        &self.host
    }

    pub fn repos(&self) -> impl Iterator<Item = &RemoteRepo> {
        self.repos.values()
    }

    pub fn resolve(&self, value: &str) -> io::Result<(&RemoteRepo, String)> {
        let safe = safe_relative(value)?;
        let components: Vec<_> = safe
            .components()
            .filter_map(|component| match component {
                Component::Normal(value) => value.to_str(),
                _ => None,
            })
            .collect();
        if components.len() < 3 {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                format!("remote selection must be CATEGORY/ID/PATH: {value}"),
            ));
        }
        let root = format!("{}/{}", components[0], components[1]);
        let repo = self.repos.get(&root).ok_or_else(|| {
            io::Error::new(
                io::ErrorKind::NotFound,
                format!("unknown remote repository: {root}"),
            )
        })?;
        Ok((repo, components[2..].join("/")))
    }
}

fn sageox_data_root() -> io::Result<PathBuf> {
    if let Some(value) = std::env::var_os("XDG_DATA_HOME") {
        return Ok(PathBuf::from(value).join("sageox"));
    }
    let home = std::env::var_os("HOME")
        .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "HOME is not set"))?;
    Ok(PathBuf::from(home).join(".local/share/sageox"))
}

fn endpoint_for_git_host(host: &str) -> io::Result<&str> {
    match host {
        "git.sageox.ai" => Ok("sageox.ai"),
        "git.test.sageox.ai" => Ok("test.sageox.ai"),
        _ => Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            format!("unsupported SageOx git host: {host}"),
        )),
    }
}

fn read_origin(path: &Path) -> io::Result<Option<String>> {
    let content = match fs::read_to_string(path) {
        Ok(content) => content,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(error),
    };
    let mut in_origin = false;
    for raw in content.lines() {
        let line = raw.trim();
        if line.starts_with('[') {
            in_origin = line == r#"[remote "origin"]"#;
            continue;
        }
        if in_origin
            && let Some((key, value)) = line.split_once('=')
            && key.trim() == "url"
        {
            return Ok(Some(value.trim().to_owned()));
        }
    }
    Ok(None)
}

fn sanitize_origin(value: &str, expected_host: &str) -> io::Result<Url> {
    let mut url = Url::parse(value).map_err(io::Error::other)?;
    if url.scheme() != "https" || url.host_str() != Some(expected_host) {
        return Err(io::Error::new(
            io::ErrorKind::PermissionDenied,
            format!("remote is not HTTPS on {expected_host}"),
        ));
    }
    let _ = url.set_username("");
    let _ = url.set_password(None);
    url.set_query(None);
    url.set_fragment(None);
    Ok(url)
}

fn safe_relative(value: &str) -> io::Result<PathBuf> {
    let path = Path::new(value);
    if path.as_os_str().is_empty()
        || path
            .components()
            .any(|part| !matches!(part, Component::Normal(_) | Component::CurDir))
    {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            format!("selection must be a safe relative path: {value}"),
        ));
    }
    Ok(path.to_owned())
}

#[derive(Clone)]
pub struct RemoteClient {
    username: String,
    password: String,
}

impl std::fmt::Debug for RemoteClient {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("RemoteClient").finish_non_exhaustive()
    }
}

impl RemoteClient {
    pub fn for_host(host: &str) -> io::Result<Arc<Self>> {
        endpoint_for_git_host(host)?;
        let (username, password) = load_credentials(host)?;
        Ok(Arc::new(Self { username, password }))
    }

    fn easy(&self, url: &Url) -> io::Result<Easy> {
        let mut easy = Easy::new();
        easy.url(url.as_str()).map_err(io::Error::other)?;
        easy.username(&self.username).map_err(io::Error::other)?;
        easy.password(&self.password).map_err(io::Error::other)?;
        let mut auth = Auth::new();
        auth.basic(true);
        easy.http_auth(&auth).map_err(io::Error::other)?;
        easy.follow_location(false).map_err(io::Error::other)?;
        easy.connect_timeout(std::time::Duration::from_secs(10))
            .map_err(io::Error::other)?;
        easy.timeout(std::time::Duration::from_secs(300))
            .map_err(io::Error::other)?;
        easy.useragent("oxfs/0.1").map_err(io::Error::other)?;
        Ok(easy)
    }

    fn request(
        &self,
        url: &Url,
        post: Option<&[u8]>,
        headers: &BTreeMap<String, String>,
    ) -> io::Result<HttpResponse> {
        let mut body = Vec::new();
        let status;
        {
            let mut easy = self.easy(url)?;
            let mut header_list = List::new();
            for (name, value) in headers {
                header_list
                    .append(&format!("{name}: {value}"))
                    .map_err(io::Error::other)?;
            }
            if !headers.is_empty() {
                easy.http_headers(header_list).map_err(io::Error::other)?;
            }
            if let Some(data) = post {
                easy.post(true).map_err(io::Error::other)?;
                easy.post_fields_copy(data).map_err(io::Error::other)?;
            }
            {
                let mut transfer = easy.transfer();
                transfer
                    .write_function(|data| {
                        body.extend_from_slice(data);
                        Ok(data.len())
                    })
                    .map_err(io::Error::other)?;
                transfer.perform().map_err(io::Error::other)?;
            }
            status = easy.response_code().map_err(io::Error::other)?;
        }
        Ok(HttpResponse { status, body })
    }

    fn download_to(
        &self,
        url: &Url,
        headers: &BTreeMap<String, String>,
        output: &mut dyn Write,
    ) -> io::Result<()> {
        let mut easy = self.easy(url)?;
        let mut header_list = List::new();
        for (name, value) in headers {
            header_list
                .append(&format!("{name}: {value}"))
                .map_err(io::Error::other)?;
        }
        if !headers.is_empty() {
            easy.http_headers(header_list).map_err(io::Error::other)?;
        }
        let mut write_error = None;
        let perform = {
            let mut transfer = easy.transfer();
            transfer
                .write_function(|data| match output.write_all(data) {
                    Ok(()) => Ok(data.len()),
                    Err(error) => {
                        write_error = Some(error);
                        Err(WriteError::Pause)
                    }
                })
                .map_err(io::Error::other)?;
            transfer.perform()
        };
        if let Some(error) = write_error {
            return Err(error);
        }
        perform.map_err(io::Error::other)?;
        let status = easy.response_code().map_err(io::Error::other)?;
        if !(200..300).contains(&status) {
            return Err(http_status("download", status));
        }
        Ok(())
    }

    pub fn fetch_raw(&self, repo: &RemoteRepo, path: &str) -> io::Result<Option<Vec<u8>>> {
        let response = self.request(&raw_url(repo, path)?, None, &BTreeMap::new())?;
        match response.status {
            200..=299 => Ok(Some(response.body)),
            404 => Ok(None),
            401 | 403 => {
                let detail = String::from_utf8_lossy(&response.body)
                    .split_whitespace()
                    .take(12)
                    .collect::<Vec<_>>()
                    .join(" ");
                Err(io::Error::new(
                    io::ErrorKind::PermissionDenied,
                    format!(
                        "remote denied access to {}/{} (HTTP {}: {})",
                        repo.virtual_root, path, response.status, detail
                    ),
                ))
            }
            status => Err(http_status("raw file", status)),
        }
    }

    pub fn fetch_raw_to(
        &self,
        repo: &RemoteRepo,
        path: &str,
        output: &mut dyn Write,
    ) -> io::Result<()> {
        self.download_to(&raw_url(repo, path)?, &BTreeMap::new(), output)
    }

    pub fn fetch_archive(&self, repo: &RemoteRepo, path: &str) -> io::Result<Vec<u8>> {
        let response = self.request(&archive_url(repo, path)?, None, &BTreeMap::new())?;
        if !(200..300).contains(&response.status) {
            return Err(http_status("directory archive", response.status));
        }
        Ok(response.body)
    }

    pub fn fetch_lfs(
        &self,
        repo: &RemoteRepo,
        pointer: &LfsPointer,
        output: &mut dyn Write,
    ) -> io::Result<()> {
        let batch_url = lfs_batch_url(repo)?;
        let request = BatchRequest {
            operation: "download",
            transfers: ["basic"],
            objects: [BatchObject {
                oid: &pointer.oid,
                size: pointer.size,
            }],
        };
        let json = serde_json::to_vec(&request).map_err(io::Error::other)?;
        let headers = BTreeMap::from([
            ("Accept".into(), "application/vnd.git-lfs+json".into()),
            ("Content-Type".into(), "application/vnd.git-lfs+json".into()),
        ]);
        let response = self.request(&batch_url, Some(&json), &headers)?;
        if !(200..300).contains(&response.status) {
            return Err(http_status("LFS batch", response.status));
        }
        let batch: BatchResponse =
            serde_json::from_slice(&response.body).map_err(io::Error::other)?;
        let object = batch.objects.into_iter().next().ok_or_else(|| {
            io::Error::new(io::ErrorKind::InvalidData, "LFS batch returned no object")
        })?;
        if let Some(error) = object.error {
            return Err(io::Error::other(format!("LFS batch: {}", error.message)));
        }
        if object.oid != pointer.oid || object.size != pointer.size {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "LFS batch object mismatch",
            ));
        }
        let action = object
            .actions
            .and_then(|actions| actions.download)
            .ok_or_else(|| {
                io::Error::new(io::ErrorKind::NotFound, "LFS object has no download action")
            })?;
        let action_url = Url::parse(&action.href).map_err(io::Error::other)?;
        if action_url.scheme() != "https" || action_url.host_str() != batch_url.host_str() {
            return Err(io::Error::new(
                io::ErrorKind::PermissionDenied,
                "LFS download action changed scheme or host",
            ));
        }
        self.download_to(&action_url, &action.header, output)
    }
}

struct HttpResponse {
    status: u32,
    body: Vec<u8>,
}

fn load_credentials(host: &str) -> io::Result<(String, String)> {
    let mut child = Command::new("ox")
        .args(["git-credential-helper", "get"])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()?;
    child
        .stdin
        .take()
        .ok_or_else(|| io::Error::other("credential helper stdin unavailable"))?
        .write_all(format!("protocol=https\nhost={host}\n\n").as_bytes())?;
    let output = child.wait_with_output()?;
    if !output.status.success() {
        return Err(io::Error::other("ox credential helper failed"));
    }
    let text = String::from_utf8(output.stdout).map_err(io::Error::other)?;
    let mut username = None;
    let mut password = None;
    for line in text.lines() {
        if let Some(value) = line.strip_prefix("username=") {
            username = Some(value.to_owned());
        } else if let Some(value) = line.strip_prefix("password=") {
            password = Some(value.to_owned());
        }
    }
    let password = password.filter(|value| !value.is_empty()).ok_or_else(|| {
        io::Error::new(
            io::ErrorKind::PermissionDenied,
            format!("no SageOx git credential for {host}; run ox login"),
        )
    })?;
    Ok((username.unwrap_or_else(|| "oauth2".into()), password))
}

fn raw_url(repo: &RemoteRepo, path: &str) -> io::Result<Url> {
    let host = repo
        .origin
        .host_str()
        .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidData, "remote has no host"))?;
    let project_path = repo
        .origin
        .path()
        .trim_matches('/')
        .trim_end_matches(".git");
    let mut url = Url::parse(&format!("https://{host}/api/v4")).map_err(io::Error::other)?;
    {
        let mut segments = url
            .path_segments_mut()
            .map_err(|_| io::Error::other("remote URL cannot hold path segments"))?;
        segments.extend(["projects", project_path, "repository", "files", path, "raw"]);
    }
    url.query_pairs_mut()
        .append_pair("ref", "HEAD")
        .append_pair("lfs", "false");
    Ok(url)
}

fn archive_url(repo: &RemoteRepo, path: &str) -> io::Result<Url> {
    let host = repo
        .origin
        .host_str()
        .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidData, "remote has no host"))?;
    let project_path = repo
        .origin
        .path()
        .trim_matches('/')
        .trim_end_matches(".git");
    let mut url = Url::parse(&format!("https://{host}/api/v4")).map_err(io::Error::other)?;
    {
        let mut segments = url
            .path_segments_mut()
            .map_err(|_| io::Error::other("remote URL cannot hold path segments"))?;
        segments.extend(["projects", project_path, "repository", "archive.tar.gz"]);
    }
    url.query_pairs_mut()
        .append_pair("sha", "HEAD")
        .append_pair("path", path)
        .append_pair("include_lfs_blobs", "false");
    Ok(url)
}

fn lfs_batch_url(repo: &RemoteRepo) -> io::Result<Url> {
    let mut url = repo.origin.clone();
    url.path_segments_mut()
        .map_err(|_| io::Error::other("remote URL cannot hold path segments"))?
        .extend(["info", "lfs", "objects", "batch"]);
    Ok(url)
}

fn http_status(operation: &str, status: u32) -> io::Error {
    io::Error::other(format!("{operation} returned HTTP {status}"))
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct LfsPointer {
    pub oid: String,
    pub size: u64,
}

pub fn parse_lfs_pointer(bytes: &[u8]) -> io::Result<Option<LfsPointer>> {
    if bytes.len() > MAX_POINTER_BYTES {
        return Ok(None);
    }
    let text = match std::str::from_utf8(bytes) {
        Ok(text) => text,
        Err(_) => return Ok(None),
    };
    let mut lines = text.lines();
    if lines.next() != Some(LFS_POINTER_VERSION) {
        return Ok(None);
    }
    let mut oid = None;
    let mut size = None;
    for line in lines {
        if let Some(value) = line.strip_prefix("oid sha256:") {
            if value.len() == 64 && value.bytes().all(|byte| byte.is_ascii_hexdigit()) {
                oid = Some(value.to_ascii_lowercase());
            }
        } else if let Some(value) = line.strip_prefix("size ") {
            size = value.parse::<u64>().ok().filter(|value| *value > 0);
        }
    }
    match (oid, size) {
        (Some(oid), Some(size)) => Ok(Some(LfsPointer { oid, size })),
        _ => Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "malformed Git LFS pointer",
        )),
    }
}

#[derive(Clone, Debug)]
pub enum RemoteFetch {
    Raw {
        client: Arc<RemoteClient>,
        repo: RemoteRepo,
        path: String,
    },
    Lfs {
        client: Arc<RemoteClient>,
        repo: RemoteRepo,
        pointer: LfsPointer,
    },
}

impl RemoteFetch {
    pub fn fetch(&self, output: &mut dyn Write) -> io::Result<()> {
        match self {
            Self::Raw { client, repo, path } => client.fetch_raw_to(repo, path, output),
            Self::Lfs {
                client,
                repo,
                pointer,
            } => client.fetch_lfs(repo, pointer, output),
        }
    }
}

#[derive(Debug)]
pub struct RemoteFile {
    pub virtual_path: String,
    pub repo: RemoteRepo,
    pub repo_path: String,
    pub bytes: Vec<u8>,
    pub pointer: Option<LfsPointer>,
    pub mode: u32,
    pub mtime_secs: u64,
}

pub fn resolve_selection(
    registry: &RemoteRegistry,
    client: &Arc<RemoteClient>,
    selection: &str,
    max_bytes: u64,
) -> io::Result<Vec<RemoteFile>> {
    let (repo, repo_path) = registry.resolve(selection)?;
    if let Some(bytes) = client.fetch_raw(repo, &repo_path)? {
        if bytes.len() as u64 > max_bytes {
            return Err(io::Error::other("selected file exceeds cache capacity"));
        }
        let pointer = parse_lfs_pointer(&bytes)?;
        return Ok(vec![RemoteFile {
            virtual_path: selection.to_owned(),
            repo: repo.clone(),
            repo_path,
            bytes,
            pointer,
            mode: 0o444,
            mtime_secs: 0,
        }]);
    }
    let response = client.fetch_archive(repo, &repo_path)?;
    unpack_archive(repo, &repo_path, &response, max_bytes)
}

fn unpack_archive(
    repo: &RemoteRepo,
    selected: &str,
    response: &[u8],
    max_bytes: u64,
) -> io::Result<Vec<RemoteFile>> {
    let decoder = GzDecoder::new(response);
    unpack_archive_reader(repo, selected, decoder, max_bytes)
}

fn unpack_archive_reader(
    repo: &RemoteRepo,
    selected: &str,
    reader: impl Read,
    max_bytes: u64,
) -> io::Result<Vec<RemoteFile>> {
    let mut archive = Archive::new(reader);
    let mut files = Vec::new();
    let mut seen = BTreeSet::new();
    let mut expanded = 0u64;
    for entry in archive.entries()? {
        let mut entry = entry?;
        let kind = entry.header().entry_type();
        if kind.is_dir()
            || kind.is_pax_global_extensions()
            || kind.is_pax_local_extensions()
            || kind.is_gnu_longname()
            || kind.is_gnu_longlink()
        {
            continue;
        }
        if !kind.is_file() {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "archive contains a link or special file",
            ));
        }
        let archive_path = entry.path()?.into_owned();
        let mut components = archive_path.components();
        let Some(Component::Normal(_root)) = components.next() else {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "archive entry has no root",
            ));
        };
        let relative: PathBuf = components.collect();
        let relative = relative.to_str().ok_or_else(|| {
            io::Error::new(io::ErrorKind::InvalidData, "archive path is not UTF-8")
        })?;
        safe_relative(relative)?;
        if relative != selected && !relative.starts_with(&format!("{selected}/")) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "archive escaped selected directory",
            ));
        }
        if !seen.insert(relative.to_owned()) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "archive contains duplicate paths",
            ));
        }
        let declared = entry.header().size()?;
        expanded = expanded
            .checked_add(declared)
            .ok_or_else(|| io::Error::other("archive size overflow"))?;
        if expanded > max_bytes {
            return Err(io::Error::other(
                "selected directory exceeds cache capacity",
            ));
        }
        let mode = entry.header().mode().unwrap_or(0o444) & 0o555;
        let mtime_secs = entry.header().mtime().unwrap_or(0);
        let mut bytes = Vec::with_capacity(usize::try_from(declared).unwrap_or(0));
        (&mut entry)
            .take(declared.saturating_add(1))
            .read_to_end(&mut bytes)?;
        if bytes.len() as u64 != declared {
            return Err(io::Error::new(
                io::ErrorKind::UnexpectedEof,
                "archive entry size mismatch",
            ));
        }
        let pointer = parse_lfs_pointer(&bytes)?;
        let virtual_path = format!("{}/{}", repo.virtual_root, relative);
        files.push(RemoteFile {
            virtual_path,
            repo: repo.clone(),
            repo_path: relative.to_owned(),
            bytes,
            pointer,
            mode,
            mtime_secs,
        });
    }
    if files.is_empty() {
        return Err(io::Error::new(
            io::ErrorKind::NotFound,
            "remote directory is empty or missing",
        ));
    }
    files.sort_by(|a, b| a.virtual_path.cmp(&b.virtual_path));
    Ok(files)
}

#[derive(Serialize)]
struct BatchRequest<'a> {
    operation: &'a str,
    transfers: [&'a str; 1],
    objects: [BatchObject<'a>; 1],
}

#[derive(Serialize)]
struct BatchObject<'a> {
    oid: &'a str,
    size: u64,
}

#[derive(Deserialize)]
struct BatchResponse {
    objects: Vec<BatchResponseObject>,
}

#[derive(Deserialize)]
struct BatchResponseObject {
    oid: String,
    size: u64,
    actions: Option<BatchActions>,
    error: Option<BatchError>,
}

#[derive(Deserialize)]
struct BatchActions {
    download: Option<BatchAction>,
}

#[derive(Deserialize)]
struct BatchAction {
    href: String,
    #[serde(default)]
    header: BTreeMap<String, String>,
}

#[derive(Deserialize)]
struct BatchError {
    message: String,
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Cursor;
    use tar::{Builder, Header};

    #[test]
    fn parses_pointer() {
        let oid = "a".repeat(64);
        let bytes = format!("{LFS_POINTER_VERSION}\noid sha256:{oid}\nsize 42\n");
        assert_eq!(
            parse_lfs_pointer(bytes.as_bytes()).unwrap(),
            Some(LfsPointer { oid, size: 42 })
        );
        assert_eq!(parse_lfs_pointer(b"ordinary").unwrap(), None);
    }

    #[test]
    fn registry_filters_and_resolves() {
        let root =
            std::env::temp_dir().join(format!("oxdir-remote-registry-{}", std::process::id()));
        let _ = fs::remove_dir_all(&root);
        let repo = root.join("teams/team_one/.git");
        fs::create_dir_all(&repo).unwrap();
        fs::write(
            repo.join("config"),
            "[remote \"origin\"]\n\turl = https://oauth2:secret@git.test.sageox.ai/group/team-context.git\n",
        )
        .unwrap();
        let registry = RemoteRegistry::discover_at("git.test.sageox.ai", &root).unwrap();
        let (repo, path) = registry.resolve("teams/team_one/docs/README.md").unwrap();
        assert_eq!(path, "docs/README.md");
        assert_eq!(
            repo.origin.as_str(),
            "https://git.test.sageox.ai/group/team-context.git"
        );
        assert!(registry.resolve("teams/team_one/../secret").is_err());
        fs::remove_dir_all(root).unwrap();
    }

    fn test_repo() -> RemoteRepo {
        RemoteRepo {
            virtual_root: "teams/team_one".into(),
            origin: Url::parse("https://git.test.sageox.ai/group/team-context.git").unwrap(),
        }
    }

    #[test]
    fn unpacks_only_selected_regular_files() {
        let mut tar = Builder::new(Vec::new());
        let bytes = b"hello";
        let mut header = Header::new_gnu();
        header.set_size(bytes.len() as u64);
        header.set_mode(0o644);
        header.set_mtime(42);
        header.set_cksum();
        tar.append_data(
            &mut header,
            "team-context-HEAD-deadbeef/docs/a.txt",
            bytes.as_slice(),
        )
        .unwrap();
        let tar = tar.into_inner().unwrap();
        let files = unpack_archive_reader(&test_repo(), "docs", Cursor::new(tar), 1024).unwrap();
        assert_eq!(files.len(), 1);
        assert_eq!(files[0].virtual_path, "teams/team_one/docs/a.txt");
        assert_eq!(files[0].bytes, bytes);
        assert_eq!(files[0].mode, 0o444);
        assert_eq!(files[0].mtime_secs, 42);
    }

    #[test]
    fn rejects_archive_links_and_capacity_overflow() {
        let mut tar = Builder::new(Vec::new());
        let mut header = Header::new_gnu();
        header.set_entry_type(tar::EntryType::Symlink);
        header.set_size(0);
        header.set_mode(0o777);
        header.set_link_name("../../secret").unwrap();
        header.set_cksum();
        tar.append_data(
            &mut header,
            "team-context-HEAD-deadbeef/docs/link",
            io::empty(),
        )
        .unwrap();
        let tar = tar.into_inner().unwrap();
        assert!(unpack_archive_reader(&test_repo(), "docs", Cursor::new(tar), 1024).is_err());

        let mut tar = Builder::new(Vec::new());
        let bytes = b"too large";
        let mut header = Header::new_gnu();
        header.set_size(bytes.len() as u64);
        header.set_mode(0o444);
        header.set_cksum();
        tar.append_data(
            &mut header,
            "team-context-HEAD-deadbeef/docs/a.txt",
            bytes.as_slice(),
        )
        .unwrap();
        let tar = tar.into_inner().unwrap();
        assert!(unpack_archive_reader(&test_repo(), "docs", Cursor::new(tar), 2).is_err());
    }

    #[test]
    #[ignore = "requires test.sageox.ai login and synced test registry"]
    fn live_test_host_reads_team_file_and_directory() {
        let registry = RemoteRegistry::discover("git.test.sageox.ai").unwrap();
        let client = RemoteClient::for_host("git.test.sageox.ai").unwrap();
        let file = resolve_selection(
            &registry,
            &client,
            "teams/team_jihjpfkt8b/SOUL.md",
            16 * 1024 * 1024,
        )
        .unwrap();
        assert_eq!(file.len(), 1);
        assert!(file[0].pointer.is_none());
        let docs = resolve_selection(
            &registry,
            &client,
            "teams/team_jihjpfkt8b/docs",
            128 * 1024 * 1024,
        )
        .unwrap();
        assert!(!docs.is_empty());
    }

    #[test]
    #[ignore = "authenticated production transport smoke test"]
    fn live_production_raw_archive_and_lfs_transport() {
        let registry = RemoteRegistry::discover("git.sageox.ai").unwrap();
        let client = RemoteClient::for_host("git.sageox.ai").unwrap();
        let file = resolve_selection(
            &registry,
            &client,
            "teams/team_jihjpfkt8b/SOUL.md",
            16 * 1024 * 1024,
        )
        .unwrap();
        assert_eq!(file.len(), 1);
        assert!(file[0].pointer.is_none());

        let docs = resolve_selection(
            &registry,
            &client,
            "teams/team_jihjpfkt8b/docs",
            128 * 1024 * 1024,
        )
        .unwrap();
        assert!(!docs.is_empty());

        let lfs = resolve_selection(
            &registry,
            &client,
            "teams/team_jihjpfkt8b/content-system/ses_019daddf-989d-7637-9a15-d8b4a2d5f1e8/attachments/4f8d2dfe_write-feature-doc.skill",
            16 * 1024 * 1024,
        )
        .unwrap();
        let pointer = lfs[0].pointer.clone().expect("expected LFS pointer");
        let mut hydrated = Vec::new();
        client
            .fetch_lfs(&lfs[0].repo, &pointer, &mut hydrated)
            .unwrap();
        assert_eq!(hydrated.len() as u64, pointer.size);
        let reference = oxfs::ContentRef::for_sha256("live", &hydrated);
        assert_eq!(reference.digest, pointer.oid);
    }
}
