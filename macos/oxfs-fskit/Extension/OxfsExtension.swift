import ExtensionFoundation
import FSKit
import OxfsFSKit

@available(macOS 26.0, *)
@main
struct OxfsExtension: UnaryFileSystemExtension {
    var fileSystem: FSUnaryFileSystem & FSUnaryFileSystemOperations {
        OxfsFileSystem()
    }
}
