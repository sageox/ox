import FileProvider
import Foundation
import OxfsCore

final class ProviderEnumerator: NSObject, NSFileProviderEnumerator {
    private let model: ProviderModel
    private let container: NSFileProviderItemIdentifier

    init(model: ProviderModel, container: NSFileProviderItemIdentifier) {
        self.model = model
        self.container = container
    }

    func invalidate() {}

    func enumerateItems(
        for observer: NSFileProviderEnumerationObserver,
        startingAt page: NSFileProviderPage
    ) {
        model.recordEnumeration()
        do {
            try model.refresh()
        } catch {
            observer.finishEnumeratingWithError(error)
            return
        }
        if container == .workingSet {
            let items = model.workspaceSnapshot().nodes.values
                .filter { $0.inode != ROOT_INODE }
                .sorted { $0.path < $1.path }
                .map(model.item)
            observer.didEnumerate(items)
            observer.finishEnumerating(upTo: nil)
            return
        }
        guard let parent = model.node(for: container), parent.kind == .directory else {
            observer.finishEnumeratingWithError(
                NSError.fileProviderErrorForNonExistentItem(withIdentifier: container)
            )
            return
        }
        let snapshot = model.workspaceSnapshot()
        let items = parent.sortedChildren.compactMap { snapshot.get($0.inode) }.map(model.item)
        observer.didEnumerate(items)
        observer.finishEnumerating(upTo: nil)
    }

    func enumerateChanges(
        for observer: NSFileProviderChangeObserver,
        from anchor: NSFileProviderSyncAnchor
    ) {
        model.recordEnumeration()
        do {
            try model.refresh()
        } catch {
            observer.finishEnumeratingWithError(error)
            return
        }
        let nextAnchor = model.syncAnchor()
        if anchor == nextAnchor {
            observer.finishEnumeratingChanges(upTo: nextAnchor, moreComing: false)
            return
        }
        let items = model.workspaceSnapshot().nodes.values
            .filter { $0.inode != ROOT_INODE }
            .map(model.item)
        observer.didUpdate(items)
        let removed = model.removedIdentifiers()
        if !removed.isEmpty {
            observer.didDeleteItems(withIdentifiers: removed)
        }
        observer.finishEnumeratingChanges(
            upTo: nextAnchor,
            moreComing: false
        )
    }

    func currentSyncAnchor(
        completionHandler: @escaping (NSFileProviderSyncAnchor?) -> Void
    ) {
        completionHandler(model.syncAnchor())
    }
}
