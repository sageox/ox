import Foundation
import FSKit
import OxfsCore
import OSLog

@available(macOS 26.0, *)
public final class OxfsFileSystem: FSUnaryFileSystem, FSUnaryFileSystemOperations {
    private let logger = Logger(subsystem: "ai.sageox.oxfs", category: "FSKit")

    public override init() { super.init() }

    public func probeResource(
        resource: FSResource,
        replyHandler: @escaping (FSProbeResult?, (any Error)?) -> Void
    ) {
        guard let path = resource as? FSPathURLResource,
              (try? MountConfiguration.load(from: path.url)) != nil else {
            replyHandler(.notRecognized, nil)
            return
        }
        let identifier = MountConfiguration.identifierUUID(for: path.url)
        logger.debug("operation=probe result=usable")
        replyHandler(.usable(
            name: "oxfs",
            containerID: FSContainerIdentifier(uuid: identifier)
        ), nil)
    }

    public func loadResource(
        resource: FSResource,
        options: FSTaskOptions,
        replyHandler: @escaping (FSVolume?, (any Error)?) -> Void
    ) {
        do {
            guard let path = resource as? FSPathURLResource else { throw POSIXError(.EINVAL) }
            let config = try MountConfiguration.load(from: path.url)
            let source = try LocalDirectorySource(root: config.sourceURL())
            let workspace = try Workspace.open(
                root: config.stateURL(relativeTo: path.url), source: source,
                config: CacheConfig(maxBytes: config.cacheBytes)
            )
            let identifier = FSContainerIdentifier(uuid: MountConfiguration.identifierUUID(for: path.url))
            containerStatus = .ready
            logger.debug("operation=load result=success")
            replyHandler(OxfsVolume(
                resource: resource,
                workspace: workspace,
                volumeID: identifier.volumeIdentifier,
                onActivate: { [weak self] in self?.containerStatus = .active },
                onDeactivate: { [weak self] in self?.containerStatus = .ready }
            ), nil)
        } catch {
            logger.error("operation=load result=error code=\((error as NSError).code, privacy: .public)")
            replyHandler(nil, error)
        }
    }

    public func unloadResource(
        resource: FSResource,
        options: FSTaskOptions,
        replyHandler: @escaping ((any Error)?) -> Void
    ) {
        containerStatus = .notReady(status: POSIXError(.ENXIO))
        logger.debug("operation=unload result=success")
        replyHandler(nil)
    }
}
