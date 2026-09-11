import FileProvider
import Foundation
import OxfsCore
import UniformTypeIdentifiers

final class ProviderItem: NSObject, NSFileProviderItem {
    private let node: Node
    private let generation: UInt64

    init(node: Node, generation: UInt64) {
        self.node = node
        self.generation = generation
    }

    var itemIdentifier: NSFileProviderItemIdentifier {
        node.inode == ROOT_INODE
            ? .rootContainer
            : NSFileProviderItemIdentifier("inode:\(node.inode)")
    }

    var parentItemIdentifier: NSFileProviderItemIdentifier {
        node.parent == ROOT_INODE
            ? .rootContainer
            : NSFileProviderItemIdentifier("inode:\(node.parent)")
    }

    var filename: String {
        node.inode == ROOT_INODE ? "oxFS" : node.name
    }

    var contentType: UTType {
        if node.kind == .directory { return .folder }
        return UTType(filenameExtension: (node.name as NSString).pathExtension) ?? .data
    }

    var documentSize: NSNumber? {
        node.kind == .file ? NSNumber(value: node.size) : nil
    }

    var contentModificationDate: Date? {
        Date(timeIntervalSince1970: TimeInterval(node.mtimeSecs))
    }

    var capabilities: NSFileProviderItemCapabilities {
        node.kind == .directory ? [.allowsReading, .allowsContentEnumerating] : [.allowsReading]
    }

    var itemVersion: NSFileProviderItemVersion {
        let content = node.file?.content.digest ?? "directory"
        return NSFileProviderItemVersion(
            contentVersion: Data(content.utf8),
            metadataVersion: Data("\(generation):\(node.inode):\(node.name)".utf8)
        )
    }

    var fileSystemFlags: NSFileProviderFileSystemFlags {
        [.userReadable]
    }
}
