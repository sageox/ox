import Testing
@testable import OxfsCore

@Suite struct PathTests {
    /// Port of Rust `path::tests::rejects_unsafe_and_reserved_paths`.
    @Test func rejectsUnsafeAndReservedPaths() {
        for bad in ["", "/a", "a/", "a//b", "a/../b", ".", ".sageox/INDEX.md"] {
            #expect(throws: PathError.self, "accepted \(bad)") {
                _ = try RelativePath.parse(bad)
            }
        }
        #expect((try? RelativePath.parse("a/b.txt"))?.asString == "a/b.txt")
    }

    @Test func componentsFileNameAndParent() throws {
        let p = try RelativePath.parse("a/b/c.txt")
        #expect(p.components() == ["a", "b", "c.txt"])
        #expect(p.fileName == "c.txt")
        #expect(p.parent == "a/b")
        let top = try RelativePath.parse("top.txt")
        #expect(top.parent == nil)
        #expect(top.fileName == "top.txt")
    }
}
