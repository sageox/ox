import Testing
@testable import OxfsCore

// Toolchain smoke test — confirms `swift test` + swift-testing run on this box.
// The substantive parity tests live in the per-module test files.
@Test func bannerMentionsParityTarget() {
    #expect(OxfsCore.banner.contains("oxfs-nfsv3"))
}
