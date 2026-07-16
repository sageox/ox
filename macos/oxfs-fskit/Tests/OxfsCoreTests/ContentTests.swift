import Testing
import Foundation
@testable import OxfsCore

@Suite struct ContentTests {
    @Test func sha256MatchesKnownVector() {
        // SHA-256("abc") is a NIST test vector.
        let ref = ContentRef.forSha256(tenant: "t", bytes: Array("abc".utf8))
        #expect(ref.digest == "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
        #expect(ref.size == 3)
        #expect(ref.storageKey == "sha256-ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
    }

    @Test func validatingInitRejectsBadReferences() {
        // Non-hex digest, empty fields, bad algorithm chars → invalidReference.
        #expect(throws: FetchError.self) {
            _ = try ContentRef(tenant: "t", algorithm: "sha256", digest: "zzzz", size: 1)
        }
        #expect(throws: FetchError.self) {
            _ = try ContentRef(tenant: "", algorithm: "sha256", digest: "ab", size: 1)
        }
        #expect(throws: FetchError.self) {
            _ = try ContentRef(tenant: "t", algorithm: "sha 256", digest: "ab", size: 1)
        }
        // A well-formed reference is accepted.
        #expect(throws: Never.self) {
            _ = try ContentRef(tenant: "t", algorithm: "sha256", digest: "abcdef01", size: 4)
        }
    }

    @Test func storageKeyLowercasesDigest() throws {
        let ref = try ContentRef(tenant: "t", algorithm: "sha256", digest: "ABCDEF", size: 3)
        #expect(ref.storageKey == "sha256-abcdef")
    }
}
