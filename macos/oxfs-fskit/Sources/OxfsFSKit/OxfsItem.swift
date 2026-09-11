import Foundation
import FSKit
import OxfsCore

@available(macOS 15.4, *)
final class OxfsItem: FSItem {
    let inode: UInt64
    let name: FSFileName
    let capturedContent: ContentRef?
    private let lock = NSLock()
    private var opened: OpenFile?
    private var openModes: FSVolume.OpenModes = []

    init(node: Node) {
        inode = node.inode
        name = FSFileName(string: node.name.isEmpty ? "/" : node.name)
        capturedContent = node.file?.content
        super.init()
    }

    func open(modes: FSVolume.OpenModes, using make: () throws -> OpenFile) throws {
        lock.lock(); defer { lock.unlock() }
        if opened == nil { opened = try make() }
        openModes.formUnion(modes)
    }

    func currentOpen() -> OpenFile? {
        lock.lock(); defer { lock.unlock() }
        return opened
    }

    func retainOpenModes(_ modes: FSVolume.OpenModes) throws {
        lock.lock()
        openModes = modes
        let closing = modes.isEmpty ? opened : nil
        if modes.isEmpty { opened = nil }
        lock.unlock()
        try closing?.close()
    }

    func reclaim() throws { try retainOpenModes([]) }

    deinit { try? opened?.close() }
}
