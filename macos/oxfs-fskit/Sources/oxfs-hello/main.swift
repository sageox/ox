import OxfsCore

// Hello-world smoke test: proves the Swift toolchain, SwiftPM, the OxfsCore
// library, and cross-target linking all work on this box.
print(OxfsCore.banner)

// Prove the ported content-identity layer links and runs end-to-end.
let ref = ContentRef.forSha256(tenant: "demo", bytes: Array("hello, oxfs-fskit".utf8))
print("sha256 storage key: \(ref.storageKey)")
print("swift dev environment: OK")
