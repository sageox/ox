import FileProvider
import Foundation
import OxfsCore

@objc(FileProviderExtension)
final class FileProviderExtension: NSObject, NSFileProviderReplicatedExtension {
    private let domain: NSFileProviderDomain
    private let manager: NSFileProviderManager
    private let model: ProviderModel?
    private let initializationError: Error?

    required init(domain: NSFileProviderDomain) {
        NSLog("oxfs-fileprovider-extension action=init status=starting domain=%@",
              domain.identifier.rawValue)
        self.domain = domain
        guard let manager = NSFileProviderManager(for: domain) else {
            fatalError("No File Provider manager for \(domain.identifier.rawValue)")
        }
        self.manager = manager
        do {
            model = try ProviderModel()
            initializationError = nil
        } catch {
            model = nil
            initializationError = error
            NSLog("oxfs-fileprovider-extension action=init status=error error=%@",
                  String(describing: error))
            if let container = FileManager.default.containerURL(
                forSecurityApplicationGroupIdentifier: oxfsAppGroup
            ) {
                try? Data(String(describing: error).utf8).write(
                    to: container.appendingPathComponent("extension-error.txt"),
                    options: .atomic
                )
            }
        }
        super.init()
        NSLog("oxfs-fileprovider-extension action=init status=%@",
              model == nil ? "unavailable" : "ready")
    }

    func invalidate() {}

    func item(
        for identifier: NSFileProviderItemIdentifier,
        request: NSFileProviderRequest,
        completionHandler: @escaping (NSFileProviderItem?, Error?) -> Void
    ) -> Progress {
        guard let model else {
            completionHandler(nil, unavailableError())
            return Progress()
        }
        guard let node = model.node(for: identifier) else {
            completionHandler(
                nil,
                NSError.fileProviderErrorForNonExistentItem(withIdentifier: identifier)
            )
            return Progress()
        }
        completionHandler(model.item(for: node), nil)
        return Progress(totalUnitCount: 1)
    }

    func fetchContents(
        for itemIdentifier: NSFileProviderItemIdentifier,
        version requestedVersion: NSFileProviderItemVersion?,
        request: NSFileProviderRequest,
        completionHandler: @escaping (URL?, NSFileProviderItem?, Error?) -> Void
    ) -> Progress {
        let progress = Progress(totalUnitCount: 100)
        guard let model else {
            completionHandler(nil, nil, unavailableError())
            return progress
        }
        guard let node = model.node(for: itemIdentifier), node.kind == .file else {
            completionHandler(
                nil,
                nil,
                NSError.fileProviderErrorForNonExistentItem(withIdentifier: itemIdentifier)
            )
            return progress
        }
        do {
            let temporary = try manager.temporaryDirectoryURL()
                .appendingPathComponent("fetch-\(UUID().uuidString)")
            FileManager.default.createFile(atPath: temporary.path, contents: nil)
            let output = try FileHandle(forWritingTo: temporary)
            defer { try? output.close() }

            let opened = try model.openInode(
                node.inode,
                expectedContent: node.file?.content
            )
            defer { try? opened.close() }
            var offset: UInt64 = 0
            while offset < node.size {
                if progress.isCancelled {
                    completionHandler(nil, nil, CocoaError(.userCancelled))
                    return progress
                }
                let bytes = try opened.read(
                    offset: offset,
                    count: Int(min(UInt64(64 * 1024), node.size - offset))
                )
                if bytes.isEmpty { break }
                try output.write(contentsOf: Data(bytes))
                offset += UInt64(bytes.count)
                progress.completedUnitCount = Int64((offset * 100) / max(node.size, 1))
            }
            try output.synchronize()
            model.recordContentFetch(bytes: offset)
            completionHandler(temporary, model.item(for: node), nil)
        } catch {
            completionHandler(
                nil,
                nil,
                NSError(
                    domain: NSCocoaErrorDomain,
                    code: NSFileReadUnknownError,
                    userInfo: [NSUnderlyingErrorKey: error]
                )
            )
        }
        return progress
    }

    func enumerator(
        for containerItemIdentifier: NSFileProviderItemIdentifier,
        request: NSFileProviderRequest
    ) throws -> NSFileProviderEnumerator {
        NSLog("oxfs-fileprovider-extension action=enumerator container=%@",
              containerItemIdentifier.rawValue)
        guard let model else {
            throw unavailableError()
        }
        return ProviderEnumerator(model: model, container: containerItemIdentifier)
    }

    private func unavailableError() -> Error {
        NSError(
            domain: NSFileProviderErrorDomain,
            code: NSFileProviderError.notAuthenticated.rawValue,
            userInfo: [
                NSLocalizedDescriptionKey:
                    "oxFS cannot access the selected folder. Select it again in OxfsFileProviderDev.",
                NSUnderlyingErrorKey: initializationError as Any
            ]
        )
    }

    func createItem(
        basedOn itemTemplate: NSFileProviderItem,
        fields: NSFileProviderItemFields,
        contents url: URL?,
        options: NSFileProviderCreateItemOptions,
        request: NSFileProviderRequest,
        completionHandler: @escaping (
            NSFileProviderItem?,
            NSFileProviderItemFields,
            Bool,
            Error?
        ) -> Void
    ) -> Progress {
        completionHandler(nil, [], false, CocoaError(.fileWriteNoPermission))
        return Progress()
    }

    func modifyItem(
        _ item: NSFileProviderItem,
        baseVersion version: NSFileProviderItemVersion,
        changedFields: NSFileProviderItemFields,
        contents newContents: URL?,
        options: NSFileProviderModifyItemOptions,
        request: NSFileProviderRequest,
        completionHandler: @escaping (
            NSFileProviderItem?,
            NSFileProviderItemFields,
            Bool,
            Error?
        ) -> Void
    ) -> Progress {
        completionHandler(nil, [], false, CocoaError(.fileWriteNoPermission))
        return Progress()
    }

    func deleteItem(
        identifier: NSFileProviderItemIdentifier,
        baseVersion version: NSFileProviderItemVersion,
        options: NSFileProviderDeleteItemOptions,
        request: NSFileProviderRequest,
        completionHandler: @escaping (Error?) -> Void
    ) -> Progress {
        completionHandler(CocoaError(.fileWriteNoPermission))
        return Progress()
    }
}
