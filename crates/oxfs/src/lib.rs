//! oxFS is a macOS-first, read-only context filesystem.
//!
//! The core deliberately knows nothing about NFS. A [`Workspace`] owns the
//! namespace, immutable content cache, stable inode allocation, and access
//! observations. The [`nfs`] module is only a protocol adapter around that API.

pub mod cache;
mod cache_catalog;
mod cache_policy;
pub mod content;
pub mod inode;
pub mod manifest;
pub mod mount;
pub mod namespace;
pub mod nfs;
pub mod observations;
pub mod path;
mod selections;
mod sha256;
pub mod telemetry;
pub mod workspace;

pub use cache::{CacheConfig, CacheTelemetrySnapshot, DEFAULT_CACHE_MAX_BYTES};
pub use cache_catalog::{CatalogSnapshot, ResidentRow};
pub use cache_policy::EvictionOrder;
pub use content::{ContentRef, ContentSource, FetchError};
pub use manifest::{Manifest, ManifestEntry};
pub use workspace::{ApplyOutcome, OpenFile, Workspace, WorkspaceError};
