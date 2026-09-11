use super::{
    Decoder, Encoder, MOUNT_PROGRAM, MOUNT_VERSION, NFS_PROGRAM, NFS_VERSION, NFS3_OK,
    NFS3ERR_BADHANDLE, NFS3ERR_INVAL, NFS3ERR_ISDIR, NFS3ERR_NAMETOOLONG, NFS3ERR_NOENT,
    NFS3ERR_NOTDIR, NFS3ERR_ROFS, rpc,
};
use crate::Workspace;
use crate::inode::ROOT_INODE;
use crate::namespace::{Node, NodeKind};
use std::io;
use std::net::{IpAddr, Ipv4Addr, SocketAddr, TcpListener, TcpStream};
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
};
use std::thread::{self, JoinHandle};
use std::time::Duration;

#[derive(Clone, Debug)]
pub struct ServerConfig {
    pub address: SocketAddr,
    pub max_record_bytes: usize,
    pub export_name: String,
}
impl Default for ServerConfig {
    fn default() -> Self {
        Self {
            address: SocketAddr::new(IpAddr::V4(Ipv4Addr::LOCALHOST), 0),
            max_record_bytes: 1024 * 1024,
            export_name: "/oxfs".into(),
        }
    }
}
pub struct NfsServer {
    workspace: Arc<Workspace>,
    config: ServerConfig,
}
pub struct ServerHandle {
    address: SocketAddr,
    stop: Arc<AtomicBool>,
    thread: Option<JoinHandle<io::Result<()>>>,
}
impl ServerHandle {
    pub fn address(&self) -> SocketAddr {
        self.address
    }
    pub fn shutdown(mut self) -> io::Result<()> {
        self.stop.store(true, Ordering::Release);
        let _ = TcpStream::connect(self.address);
        self.thread
            .take()
            .unwrap()
            .join()
            .map_err(|_| io::Error::other("NFS server panicked"))?
    }
}
impl Drop for ServerHandle {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Release);
        let _ = TcpStream::connect(self.address);
    }
}
impl NfsServer {
    pub fn new(workspace: Arc<Workspace>, config: ServerConfig) -> Self {
        Self { workspace, config }
    }
    pub fn spawn(self) -> io::Result<ServerHandle> {
        let listener = TcpListener::bind(self.config.address)?;
        let address = listener.local_addr()?;
        listener.set_nonblocking(true)?;
        let stop = Arc::new(AtomicBool::new(false));
        let child_stop = stop.clone();
        let thread = thread::Builder::new()
            .name("oxfs-nfs".into())
            .spawn(move || self.run(listener, child_stop))?;
        Ok(ServerHandle {
            address,
            stop,
            thread: Some(thread),
        })
    }
    fn run(self, listener: TcpListener, stop: Arc<AtomicBool>) -> io::Result<()> {
        while !stop.load(Ordering::Acquire) {
            match listener.accept() {
                Ok((stream, peer)) => {
                    if !peer.ip().is_loopback() {
                        continue;
                    }
                    stream.set_nonblocking(false)?;
                    let ws = self.workspace.clone();
                    let cfg = self.config.clone();
                    thread::spawn(move || {
                        if let Err(error) = serve_connection(stream, &ws, &cfg) {
                            eprintln!("level=WARN action=nfs_connection error={error:?}");
                        }
                    });
                }
                Err(e) if e.kind() == io::ErrorKind::WouldBlock => {
                    thread::sleep(Duration::from_millis(5))
                }
                Err(e) => return Err(e),
            }
        }
        Ok(())
    }
}
fn serve_connection(
    mut stream: TcpStream,
    workspace: &Arc<Workspace>,
    config: &ServerConfig,
) -> io::Result<()> {
    while let Some(record) = rpc::read_record(&mut stream, config.max_record_bytes)? {
        let reply = match rpc::decode_call(&record) {
            Ok(call) => dispatch(workspace, config, &call),
            Err(_) => rpc::accepted(0, rpc::GARBAGE_ARGS, &[]),
        };
        rpc::write_record(&mut stream, &reply)?;
    }
    Ok(())
}
fn dispatch(ws: &Arc<Workspace>, cfg: &ServerConfig, call: &rpc::Call<'_>) -> Vec<u8> {
    if call.program == NFS_PROGRAM {
        if call.version != NFS_VERSION {
            return rpc::mismatch(call.xid, NFS_VERSION, NFS_VERSION);
        }
        if call.procedure > 21 {
            return rpc::accepted(call.xid, rpc::PROC_UNAVAIL, &[]);
        }
        let body = dispatch_nfs(ws, call.procedure, call.arguments);
        return match body {
            Ok(body) => rpc::accepted(call.xid, rpc::SUCCESS, &body),
            Err(_) => rpc::accepted(call.xid, rpc::GARBAGE_ARGS, &[]),
        };
    }
    if call.program == MOUNT_PROGRAM {
        if call.version != MOUNT_VERSION {
            return rpc::mismatch(call.xid, MOUNT_VERSION, MOUNT_VERSION);
        }
        if !matches!(call.procedure, 0 | 1 | 3 | 5) {
            return rpc::accepted(call.xid, rpc::PROC_UNAVAIL, &[]);
        }
        let body = dispatch_mount(cfg, call.procedure, call.arguments);
        return match body {
            Ok(body) => rpc::accepted(call.xid, rpc::SUCCESS, &body),
            Err(_) => rpc::accepted(call.xid, rpc::GARBAGE_ARGS, &[]),
        };
    }
    rpc::accepted(call.xid, rpc::PROG_UNAVAIL, &[])
}
fn dispatch_mount(
    cfg: &ServerConfig,
    procedure: u32,
    args: &[u8],
) -> Result<Vec<u8>, super::XdrError> {
    let mut e = Encoder::new();
    match procedure {
        0 => {}
        1 => {
            let mut d = Decoder::new(args);
            let path = d.string(1024)?;
            d.finish()?;
            if path != cfg.export_name {
                e.u32(13)
            } else {
                e.u32(0);
                e.opaque(&handle(ROOT_INODE));
                e.u32(1);
                e.u32(1)
            }
        }
        3 => {} // UMNT is idempotent; there is no per-client mutable state.
        5 => {
            e.boolean(true);
            e.string(&cfg.export_name);
            e.boolean(false);
            e.boolean(false)
        }
        _ => unreachable!("procedure filtered by dispatcher"),
    }
    Ok(e.into_bytes())
}
fn dispatch_nfs(
    ws: &Arc<Workspace>,
    procedure: u32,
    args: &[u8],
) -> Result<Vec<u8>, super::XdrError> {
    match procedure {
        0 => Ok(Vec::new()),
        1 => {
            ws.record_nfs_getattr();
            getattr(ws, args)
        }
        3 => {
            ws.record_nfs_lookup();
            lookup(ws, args)
        }
        4 => {
            ws.record_nfs_access();
            access(ws, args)
        }
        5 => readlink(ws, args),
        6 => {
            ws.record_nfs_read();
            read(ws, args)
        }
        16 => {
            ws.record_nfs_readdir();
            readdir(ws, args, false)
        }
        17 => {
            ws.record_nfs_readdir();
            readdir(ws, args, true)
        }
        18 => fsstat(ws, args),
        19 => fsinfo(ws, args),
        20 => pathconf(ws, args),
        2 | 7 | 8 | 9 | 10 | 11 | 12 | 13 | 14 | 15 | 21 => Ok(read_only(procedure)),
        _ => unreachable!("NFSv3 procedures are 0 through 21"),
    }
}
fn getattr(ws: &Arc<Workspace>, args: &[u8]) -> Result<Vec<u8>, super::XdrError> {
    let mut d = Decoder::new(args);
    let ino = decode_handle(&mut d);
    let mut e = Encoder::new();
    match ino.and_then(|i| ws.snapshot().get(i).cloned()) {
        Some(node) => {
            e.u32(NFS3_OK);
            encode_attr(&mut e, &node)
        }
        None => e.u32(NFS3ERR_BADHANDLE),
    }
    Ok(e.into_bytes())
}
fn lookup(ws: &Arc<Workspace>, args: &[u8]) -> Result<Vec<u8>, super::XdrError> {
    let mut d = Decoder::new(args);
    let parent = decode_handle(&mut d);
    // filename3 is opaque octets (RFC 1813), not required to be UTF-8. Read the
    // raw bytes: a name over NFS3_MAXNAMLEN or one that is not valid UTF-8 simply
    // cannot name an entry in this UTF-8 namespace, so it is answered at the NFS
    // layer (NAMETOOLONG / NOENT) rather than propagated as an XdrError, which
    // dispatch would turn into a whole-RPC GARBAGE_ARGS the client sees as EIO.
    // A truncated wire buffer still errors here, which is the correct GARBAGE_ARGS.
    let name_bytes = d.opaque(64 * 1024)?;
    let snap = ws.snapshot();
    let mut e = Encoder::new();
    match parent.and_then(|p| snap.get(p)) {
        None => {
            e.u32(NFS3ERR_BADHANDLE);
            e.boolean(false);
        }
        Some(parent_node) if parent_node.kind == NodeKind::File => {
            e.u32(NFS3ERR_NOTDIR);
            e.boolean(true);
            encode_attr(&mut e, parent_node);
        }
        Some(parent_node) if name_bytes.len() > 255 => {
            e.u32(NFS3ERR_NAMETOOLONG);
            e.boolean(true);
            encode_attr(&mut e, parent_node);
        }
        Some(parent_node) => match std::str::from_utf8(name_bytes)
            .ok()
            .and_then(|name| snap.lookup(parent_node.inode, name))
        {
            None => {
                e.u32(NFS3ERR_NOENT);
                e.boolean(true);
                encode_attr(&mut e, parent_node);
            }
            Some(node) => {
                e.u32(NFS3_OK);
                e.opaque(&handle(node.inode));
                e.boolean(true);
                encode_attr(&mut e, node);
                e.boolean(true);
                encode_attr(&mut e, snap.get(node.parent).unwrap())
            }
        },
    }
    Ok(e.into_bytes())
}
fn access(ws: &Arc<Workspace>, args: &[u8]) -> Result<Vec<u8>, super::XdrError> {
    let mut d = Decoder::new(args);
    let ino = decode_handle(&mut d);
    let requested = d.u32()?;
    let mut e = Encoder::new();
    match ino.and_then(|i| ws.snapshot().get(i).cloned()) {
        None => e.u32(NFS3ERR_BADHANDLE),
        Some(node) => {
            e.u32(NFS3_OK);
            e.boolean(true);
            encode_attr(&mut e, &node);
            let mut allowed = 0x01;
            if node.kind == NodeKind::Directory {
                allowed |= 0x02;
            }
            if node.mode & 0o111 != 0 || node.kind == NodeKind::Directory {
                allowed |= 0x20;
            }
            e.u32(requested & allowed)
        }
    }
    Ok(e.into_bytes())
}

fn readlink(ws: &Arc<Workspace>, args: &[u8]) -> Result<Vec<u8>, super::XdrError> {
    let mut d = Decoder::new(args);
    let inode = decode_handle(&mut d);
    let mut e = Encoder::new();
    match inode.and_then(|i| ws.snapshot().get(i).cloned()) {
        None => {
            e.u32(NFS3ERR_BADHANDLE);
            e.boolean(false);
        }
        Some(node) => {
            e.u32(NFS3ERR_INVAL);
            e.boolean(true);
            encode_attr(&mut e, &node);
        }
    }
    Ok(e.into_bytes())
}
fn read(ws: &Arc<Workspace>, args: &[u8]) -> Result<Vec<u8>, super::XdrError> {
    let mut d = Decoder::new(args);
    let ino = decode_handle(&mut d);
    let offset = d.u64()?;
    let count = d.u32()?.min(1024 * 1024) as usize;
    let mut e = Encoder::new();
    let Some(ino) = ino else {
        e.u32(NFS3ERR_BADHANDLE);
        return Ok(e.into_bytes());
    };
    match ws.open_inode(ino) {
        Err(crate::WorkspaceError::IsDirectory) => e.u32(NFS3ERR_ISDIR),
        Err(_) => e.u32(NFS3ERR_NOENT),
        Ok(file) => match file.read(offset, count) {
            Err(_) => e.u32(super::NFS3ERR_IO),
            Ok(data) => {
                e.u32(NFS3_OK);
                let node = ws.snapshot().get(ino).cloned().unwrap();
                e.boolean(true);
                encode_attr(&mut e, &node);
                e.u32(data.len() as u32);
                e.boolean(offset.saturating_add(data.len() as u64) >= file.size());
                e.opaque(&data)
            }
        },
    }
    Ok(e.into_bytes())
}
fn readdir(ws: &Arc<Workspace>, args: &[u8], plus: bool) -> Result<Vec<u8>, super::XdrError> {
    let mut d = Decoder::new(args);
    let ino = decode_handle(&mut d);
    let cookie = d.u64()?;
    let _verifier = d.fixed(8)?;
    let dir_count = d.u32()? as usize;
    let max_count = if plus { d.u32()? as usize } else { dir_count };
    let snap = ws.snapshot();
    let mut e = Encoder::new();
    let Some(node) = ino.and_then(|i| snap.get(i)) else {
        e.u32(NFS3ERR_BADHANDLE);
        return Ok(e.into_bytes());
    };
    if node.kind != NodeKind::Directory {
        e.u32(NFS3ERR_NOTDIR);
        return Ok(e.into_bytes());
    }
    e.u32(NFS3_OK);
    e.boolean(true);
    encode_attr(&mut e, node);
    e.fixed(&snap.generation.to_be_bytes());
    let mut estimated = 128usize;
    let mut dir_estimated = 0usize;
    let mut eof = true;
    for (index, (name, child)) in node.children.iter().enumerate().skip(cookie as usize) {
        let entry_size = 32 + name.len().div_ceil(4) * 4 + if plus { 128 } else { 0 };
        let dir_entry_size = 32 + name.len().div_ceil(4) * 4;
        if estimated.saturating_add(entry_size) > max_count
            || dir_estimated.saturating_add(dir_entry_size) > dir_count
        {
            eof = false;
            break;
        }
        e.boolean(true);
        e.u64(*child);
        e.string(name);
        e.u64((index + 1) as u64);
        if plus {
            let child = snap.get(*child).expect("directory child exists");
            e.boolean(true);
            encode_attr(&mut e, child);
            e.boolean(true);
            e.opaque(&handle(child.inode));
        }
        estimated += entry_size;
        dir_estimated += dir_entry_size;
    }
    e.boolean(false);
    e.boolean(eof);
    Ok(e.into_bytes())
}

fn fsstat(ws: &Arc<Workspace>, args: &[u8]) -> Result<Vec<u8>, super::XdrError> {
    let mut d = Decoder::new(args);
    let inode = decode_handle(&mut d);
    let mut e = Encoder::new();
    let Some(node) = inode.and_then(|i| ws.snapshot().get(i).cloned()) else {
        e.u32(NFS3ERR_BADHANDLE);
        return Ok(e.into_bytes());
    };
    e.u32(NFS3_OK);
    e.boolean(true);
    encode_attr(&mut e, &node);
    // Rebuildable cache: report a conservative logical capacity rather than host disk totals.
    let (capacity, remaining) = ws.cache_capacity();
    e.u64(capacity);
    e.u64(remaining);
    e.u64(remaining);
    e.u64(u64::MAX / 2);
    e.u64(u64::MAX / 2);
    e.u64(u64::MAX / 2);
    e.u32(0);
    Ok(e.into_bytes())
}
fn fsinfo(ws: &Arc<Workspace>, args: &[u8]) -> Result<Vec<u8>, super::XdrError> {
    let mut d = Decoder::new(args);
    let inode = decode_handle(&mut d);
    let mut e = Encoder::new();
    let Some(node) = inode.and_then(|i| ws.snapshot().get(i).cloned()) else {
        e.u32(NFS3ERR_BADHANDLE);
        return Ok(e.into_bytes());
    };
    e.u32(NFS3_OK);
    e.boolean(true);
    encode_attr(&mut e, &node);
    for value in [
        1024 * 1024u32,
        1024 * 1024,
        4096,
        1024 * 1024,
        1024 * 1024,
        4096,
        4096,
    ] {
        e.u32(value)
    }
    e.u64(u64::MAX);
    e.u32(1);
    e.u32(0);
    e.u32(0x0008);
    Ok(e.into_bytes())
}
fn pathconf(ws: &Arc<Workspace>, args: &[u8]) -> Result<Vec<u8>, super::XdrError> {
    let mut d = Decoder::new(args);
    let inode = decode_handle(&mut d);
    let mut e = Encoder::new();
    let Some(node) = inode.and_then(|i| ws.snapshot().get(i).cloned()) else {
        e.u32(NFS3ERR_BADHANDLE);
        return Ok(e.into_bytes());
    };
    e.u32(NFS3_OK);
    e.boolean(true);
    encode_attr(&mut e, &node);
    e.u32(u32::MAX);
    e.u32(255);
    e.boolean(true);
    e.boolean(false);
    e.boolean(false);
    e.boolean(true);
    Ok(e.into_bytes())
}
fn read_only(procedure: u32) -> Vec<u8> {
    let mut e = Encoder::new();
    e.u32(NFS3ERR_ROFS);
    let booleans = match procedure {
        14 => 4,
        15 => 3,
        2 | 7 | 8 | 9 | 10 | 11 | 12 | 13 | 21 => 2,
        _ => 0,
    };
    for _ in 0..booleans {
        e.boolean(false)
    }
    e.into_bytes()
}
fn handle(inode: u64) -> [u8; 16] {
    let mut h = [0u8; 16];
    h[..8].copy_from_slice(b"OXFSv001");
    h[8..].copy_from_slice(&inode.to_be_bytes());
    h
}
fn decode_handle(d: &mut Decoder<'_>) -> Option<u64> {
    let bytes = d.opaque(64).ok()?;
    if bytes.len() != 16 || &bytes[..8] != b"OXFSv001" {
        return None;
    }
    Some(u64::from_be_bytes(bytes[8..].try_into().ok()?))
}
fn encode_attr(e: &mut Encoder, node: &Node) {
    e.u32(if node.kind == NodeKind::Directory {
        2
    } else {
        1
    });
    e.u32(node.mode);
    e.u32(1);
    e.u32(0);
    e.u32(0);
    e.u64(node.size);
    e.u64(node.size);
    e.u32(0);
    e.u32(0);
    e.u64(0);
    e.u64(node.inode);
    for _ in 0..2 {
        e.u32(node.mtime_secs as u32);
        e.u32(0)
    }
    e.u32(node.mtime_secs as u32);
    e.u32(0)
}
