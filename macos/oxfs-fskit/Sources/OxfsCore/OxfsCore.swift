/// OxfsCore — the protocol-agnostic core of oxfs-fskit.
///
/// A pure-Swift reimplementation of the `crates/oxfs` Rust core (namespace,
/// verified cache, eviction, manifest reconciliation) targeting 100% behavioral
/// parity with `oxfs-nfsv3`. It knows nothing about FSKit — the FSKit adapter
/// (a separate Xcode target) is a thin shell over this API, exactly as the Rust
/// `nfs` adapter is a thin shell over the Rust `Workspace`.
public enum OxfsCore {
    public static let version = "0.0.0-dev"
    public static let banner = "oxfs-fskit core \(version) — parity target: oxfs-nfsv3"
}
