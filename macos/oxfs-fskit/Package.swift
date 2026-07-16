// swift-tools-version: 6.0
// oxfs-fskit — a standalone Swift FSKit filesystem at parity with oxfs-nfsv3.
// The core library (OxfsCore) is pure Swift and builds/tests with the CLI
// toolchain (no Xcode). The FSKit .appex adapter is a separate Xcode target
// (needs full Xcode + signing) and is NOT built by SwiftPM.
import PackageDescription

let package = Package(
    name: "oxfs-fskit",
    platforms: [
        // FSKit floor is macOS 15.4; the core library itself has no such need,
        // but we keep the package aligned to the product's target OS.
        .macOS(.v15)
    ],
    products: [
        .library(name: "OxfsCore", targets: ["OxfsCore"]),
        .executable(name: "oxfs-hello", targets: ["oxfs-hello"]),
    ],
    targets: [
        .target(name: "OxfsCore"),
        .executableTarget(
            name: "oxfs-hello",
            dependencies: ["OxfsCore"]
        ),
        .testTarget(
            name: "OxfsCoreTests",
            dependencies: ["OxfsCore"]
        ),
    ]
)
