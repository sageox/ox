import Foundation
import FSKit
import OxfsCore
import OSLog

@available(macOS 15.4, *)
private func posix(_ code: POSIXErrorCode) -> any Error {
    fs_errorForPOSIXError(code.rawValue)
}

@available(macOS 15.4, *)
public final class OxfsVolume: FSVolume {
    private let resource: FSResource
    private let workspace: Workspace
    private let onActivate: () -> Void
    private let onDeactivate: () -> Void
    private let logger = Logger(subsystem: "ai.sageox.oxfs", category: "FSKit")

    init(resource: FSResource, workspace: Workspace, volumeID: FSVolume.Identifier,
         onActivate: @escaping () -> Void, onDeactivate: @escaping () -> Void) {
        self.resource = resource
        self.workspace = workspace
        self.onActivate = onActivate
        self.onDeactivate = onDeactivate
        super.init(
            volumeID: volumeID,
            volumeName: FSFileName(string: "oxfs")
        )
    }

    private func node(for item: FSItem) throws -> Node {
        guard let item = item as? OxfsItem,
              let node = workspace.snapshot().get(item.inode) else { throw posix(.ESTALE) }
        return node
    }

    private func attributes(for node: Node) -> FSItem.Attributes {
        let value = FSItem.Attributes()
        value.fileID = FSItem.Identifier(rawValue: node.inode) ?? .invalid
        value.parentID = node.inode == ROOT_INODE ? .parentOfRoot
            : (FSItem.Identifier(rawValue: node.parent) ?? .invalid)
        value.type = node.kind == .directory ? .directory : .file
        value.mode = (node.kind == .directory ? UInt32(S_IFDIR) : UInt32(S_IFREG)) | node.mode
        value.uid = getuid(); value.gid = getgid(); value.linkCount = 1
        value.size = node.size; value.allocSize = node.size
        let stamp = timespec(tv_sec: Int(node.mtimeSecs), tv_nsec: 0)
        value.modifyTime = stamp; value.changeTime = stamp
        value.accessTime = stamp; value.birthTime = stamp
        return value
    }
}

@available(macOS 15.4, *)
extension OxfsVolume: FSVolume.PathConfOperations {
    public var maximumLinkCount: Int { 1 }
    public var maximumNameLength: Int { 255 }
    public var restrictsOwnershipChanges: Bool { true }
    public var truncatesLongNames: Bool { false }
    public var maximumXattrSize: Int { 0 }
    public var maximumFileSize: UInt64 { UInt64.max }
}

@available(macOS 15.4, *)
extension OxfsVolume: FSVolume.Operations {
    public var supportedVolumeCapabilities: FSVolume.SupportedCapabilities {
        let c = FSVolume.SupportedCapabilities()
        c.supportsPersistentObjectIDs = true
        c.supportsHiddenFiles = true
        c.supports64BitObjectIDs = true
        c.doesNotSupportVolumeSizes = true
        c.caseFormat = .sensitive
        return c
    }

    public var volumeStatistics: FSStatFSResult {
        let s = FSStatFSResult(fileSystemTypeName: "oxfs")
        let cap = workspace.cacheCapacity()
        s.blockSize = 4096; s.ioSize = 1024 * 1024
        s.totalBlocks = cap.capacity / 4096
        s.availableBlocks = cap.remaining / 4096; s.freeBlocks = cap.remaining / 4096
        s.totalFiles = UInt64(workspace.snapshot().nodes.count); s.freeFiles = UInt64.max
        return s
    }

    public func activate(options: FSTaskOptions) async throws -> FSItem {
        guard let root = workspace.snapshot().get(ROOT_INODE) else { throw posix(.EIO) }
        onActivate()
        logger.debug("operation=activate result=success inode=\(ROOT_INODE, privacy: .public)")
        return OxfsItem(node: root)
    }
    public func deactivate(options: FSDeactivateOptions = []) async throws {
        onDeactivate()
        logger.debug("operation=deactivate result=success")
    }
    public func mount(options: FSTaskOptions) async throws {}
    public func unmount() async {}
    public func synchronize(flags: FSSyncFlags) async throws {}

    public func attributes(_ desired: FSItem.GetAttributesRequest, of item: FSItem) async throws -> FSItem.Attributes {
        attributes(for: try node(for: item))
    }
    public func setAttributes(_ request: FSItem.SetAttributesRequest, on item: FSItem) async throws -> FSItem.Attributes {
        throw posix(.EROFS)
    }
    public func lookupItem(named name: FSFileName, inDirectory directory: FSItem) async throws -> (FSItem, FSFileName) {
        guard let raw = name.string else { throw posix(.EINVAL) }
        guard let item = directory as? OxfsItem else { throw posix(.ESTALE) }
        let snapshot = workspace.snapshot()
        guard let parent = snapshot.get(item.inode) else { throw posix(.ESTALE) }
        guard parent.kind == .directory else { throw posix(.ENOTDIR) }
        guard let child = snapshot.lookup(parent: parent.inode, name: raw) else { throw posix(.ENOENT) }
        logger.debug("operation=lookup result=success inode=\(child.inode, privacy: .public)")
        return (OxfsItem(node: child), name)
    }
    public func reclaimItem(_ item: FSItem) async throws {
        if let item = item as? OxfsItem { try item.reclaim() }
    }
    public func readSymbolicLink(_ item: FSItem) async throws -> FSFileName { throw posix(.EINVAL) }
    public func createItem(named: FSFileName, type: FSItem.ItemType, inDirectory: FSItem,
                           attributes: FSItem.SetAttributesRequest) async throws -> (FSItem, FSFileName) { throw posix(.EROFS) }
    public func createSymbolicLink(named: FSFileName, inDirectory: FSItem,
                                  attributes: FSItem.SetAttributesRequest,
                                  linkContents: FSFileName) async throws -> (FSItem, FSFileName) { throw posix(.EROFS) }
    public func createLink(to: FSItem, named: FSFileName, inDirectory: FSItem) async throws -> FSFileName { throw posix(.EROFS) }
    public func removeItem(_ item: FSItem, named: FSFileName, fromDirectory: FSItem) async throws { throw posix(.EROFS) }
    public func renameItem(_ item: FSItem, inDirectory: FSItem, named: FSFileName,
                           to: FSFileName, inDirectory destination: FSItem,
                           overItem: FSItem?) async throws -> FSFileName { throw posix(.EROFS) }

    public func enumerateDirectory(_ directory: FSItem, startingAt cookie: FSDirectoryCookie,
                                   verifier: FSDirectoryVerifier,
                                   attributes requested: FSItem.GetAttributesRequest?,
                                   packer: FSDirectoryEntryPacker) async throws -> FSDirectoryVerifier {
        guard let item = directory as? OxfsItem else { throw posix(.ESTALE) }
        let snapshot = workspace.snapshot()
        guard let parent = snapshot.get(item.inode) else { throw posix(.ESTALE) }
        guard parent.kind == .directory else { throw posix(.ENOTDIR) }
        let currentVerifier = FSDirectoryVerifier(snapshot.generation)
        if verifier != .initial && verifier != currentVerifier {
            throw FSError(.invalidDirectoryCookie)
        }
        guard cookie.rawValue <= UInt64(parent.sortedChildren.count),
              let start = Int(exactly: cookie.rawValue) else {
            throw FSError(.invalidDirectoryCookie)
        }
        for (index, childRef) in parent.sortedChildren.enumerated() where index >= start {
            guard let child = snapshot.get(childRef.inode) else { throw posix(.EIO) }
            let next = FSDirectoryCookie(UInt64(index + 1))
            let packed = packer.packEntry(
                name: FSFileName(string: childRef.name),
                itemType: child.kind == .directory ? .directory : .file,
                itemID: FSItem.Identifier(rawValue: child.inode) ?? .invalid,
                nextCookie: next,
                attributes: requested == nil ? nil : attributes(for: child)
            )
            if !packed { break }
        }
        logger.debug("operation=enumerate result=success inode=\(parent.inode, privacy: .public)")
        return currentVerifier
    }
}

@available(macOS 15.4, *)
extension OxfsVolume: FSVolume.OpenCloseOperations {
    public func openItem(_ raw: FSItem, modes: FSVolume.OpenModes) async throws {
        if modes.contains(.write) { throw posix(.EROFS) }
        guard let item = raw as? OxfsItem else { throw posix(.ESTALE) }
        let node = try node(for: item)
        guard node.kind == .file else { throw posix(.EISDIR) }
        guard node.file?.content == item.capturedContent else { throw posix(.ESTALE) }
        try item.open(modes: modes) {
            try workspace.openInode(node.inode, expectedContent: item.capturedContent)
        }
        logger.debug("operation=open result=success inode=\(node.inode, privacy: .public)")
    }
    public func closeItem(_ item: FSItem, modes: FSVolume.OpenModes) async throws {
        guard let item = item as? OxfsItem else { throw posix(.ESTALE) }
        try item.retainOpenModes(modes)
        logger.debug("operation=close result=success inode=\(item.inode, privacy: .public)")
    }
}

@available(macOS 15.4, *)
extension OxfsVolume: FSVolume.ReadWriteOperations {
    public func read(from raw: FSItem, at offset: off_t, length: Int,
                     into buffer: FSMutableFileDataBuffer) async throws -> Int {
        guard offset >= 0, let item = raw as? OxfsItem,
              let opened = item.currentOpen() else { throw posix(.EBADF) }
        let bytes = try opened.read(offset: UInt64(offset), count: min(length, buffer.length))
        bytes.withUnsafeBytes { source in
            buffer.withUnsafeMutableBytes { destination in
                if let src = source.baseAddress, let dst = destination.baseAddress {
                    memcpy(dst, src, bytes.count)
                }
            }
        }
        return bytes.count
    }
    public func write(contents: Data, to item: FSItem, at offset: off_t) async throws -> Int { throw posix(.EROFS) }
}
