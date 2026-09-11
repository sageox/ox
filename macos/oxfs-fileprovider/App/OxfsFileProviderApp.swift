import AppKit
import FileProvider
import OxfsCore
import SwiftUI

private let appGroup = "C2LRSF5CK3.group.ai.sageox.oxfs.fileprovider"
private let domainIdentifier = NSFileProviderDomainIdentifier(rawValue: "oxfs")

private struct ProviderConfiguration: Codable {
    let sourceBookmark: Data
    let sourcePath: String
    let sourceName: String
    let deliveryMode: String?
    let workspaceKey: String?
    let cacheMaxBytes: UInt64?
    let cachePolicy: String?
}

fileprivate struct ProviderTelemetry: Codable {
    let generation: UInt64
    let visibleFiles: Int
    let residentObjects: UInt64
    let residentBytes: UInt64
    let fetchedObjects: UInt64
    let fetchedBytes: UInt64
    let evictedObjects: UInt64
    let blockedEvictions: UInt64
    let enumerations: UInt64
    let contentFetches: UInt64
    let contentReadBytes: UInt64
    let updatedAt: Date
}

@MainActor
final class AppModel: ObservableObject {
    @Published var status = "Choose a source directory to publish through File Provider."
    @Published var domainURL: URL?
    @Published var remoteHost = "git.sageox.ai"
    @Published var remoteSelection = ""
    @Published var cacheMiB = "1024"
    @Published var cachePolicy = "lrs"
    @Published fileprivate private(set) var telemetry: ProviderTelemetry?

    func resumeExistingSelection(resetDomain: Bool = true) {
        guard let container = FileManager.default.containerURL(
            forSecurityApplicationGroupIdentifier: appGroup
        ),
        let data = try? Data(
            contentsOf: container.appendingPathComponent("configuration.json")
        ),
        let config = try? JSONDecoder().decode(ProviderConfiguration.self, from: data)
        else { return }

        guard !config.sourcePath.hasPrefix("/remote/") else {
            status = "Use Publish Remote Selection to refresh remote content."
            return
        }
        let source = URL(fileURLWithPath: config.sourcePath)
        install(
            source: source,
            resetDomain: resetDomain,
            cacheMaxBytes: config.cacheMaxBytes,
            cachePolicy: config.cachePolicy
        )
    }

    func activateStagedWorkspace() {
        Task {
            do {
                guard let container = FileManager.default.containerURL(
                    forSecurityApplicationGroupIdentifier: appGroup
                ),
                let data = try? Data(
                    contentsOf: container.appendingPathComponent("configuration.json")
                ),
                let config = try? JSONDecoder().decode(ProviderConfiguration.self, from: data),
                config.deliveryMode == "staged"
                else {
                    throw CocoaError(.fileNoSuchFile)
                }
                let domain = NSFileProviderDomain(
                    identifier: domainIdentifier,
                    displayName: "oxFS — \(config.sourceName)"
                )
                for oldDomain in try await NSFileProviderManager.domains()
                    where oldDomain.identifier == domain.identifier {
                    try await NSFileProviderManager.remove(oldDomain)
                }
                try await NSFileProviderManager.add(domain)
                guard let manager = NSFileProviderManager(for: domain) else {
                    throw CocoaError(.featureUnsupported)
                }
                try await manager.signalEnumerator(for: .workingSet)
                try await manager.signalEnumerator(for: .rootContainer)
                domainURL = try await manager.getUserVisibleURL(for: .rootContainer)
                status = "Activated staged workspace: \(domainURL?.path ?? config.sourceName)"
            } catch {
                status = "Activation failed: \(error.localizedDescription)"
            }
        }
    }

    func install() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.allowsMultipleSelection = false
        panel.prompt = "Publish with oxFS"
        guard panel.runModal() == .OK, let source = panel.url else { return }
        // Recreating the domain is the first control-plane implementation:
        // it guarantees a fresh extension process consumes the new bookmark
        // and publishes one coherent namespace generation. Incremental domain
        // updates will replace this once change-anchor parity lands.
        install(source: source, resetDomain: true)
    }

    func install(
        source: URL,
        resetDomain: Bool,
        cacheMaxBytes: UInt64? = nil,
        cachePolicy: String? = nil
    ) {
        Task {
            do {
                let selectedMaxBytes = try resolvedCacheBytes(cacheMaxBytes)
                let selectedPolicy = cachePolicy ?? self.cachePolicy
                let selectedCache = try cacheConfiguration(
                    maxBytes: selectedMaxBytes,
                    policy: selectedPolicy
                )
                let config = ProviderConfiguration(
                    sourceBookmark: Data(),
                    sourcePath: source.path,
                    sourceName: source.lastPathComponent,
                    deliveryMode: "staged",
                    workspaceKey: source.path,
                    cacheMaxBytes: selectedMaxBytes,
                    cachePolicy: selectedPolicy
                )
                guard let container = FileManager.default.containerURL(
                    forSecurityApplicationGroupIdentifier: appGroup
                ) else {
                    throw CocoaError(.fileNoSuchFile)
                }
                try FileManager.default.createDirectory(
                    at: container,
                    withIntermediateDirectories: true
                )
                try JSONEncoder().encode(config).write(
                    to: container.appendingPathComponent("configuration.json"),
                    options: .atomic
                )
                status = "Publishing \(source.lastPathComponent)…"
                let delta = try LocalSnapshotPublisher.publish(
                    source: source,
                    container: container,
                    cacheConfig: selectedCache
                )

                let domain = NSFileProviderDomain(
                    identifier: domainIdentifier,
                    displayName: "oxFS — \(source.lastPathComponent)"
                )
                let existing = try await NSFileProviderManager.domains()
                if resetDomain {
                    for oldDomain in existing where oldDomain.identifier == domain.identifier {
                        try await NSFileProviderManager.remove(oldDomain)
                    }
                    try await NSFileProviderManager.add(domain)
                } else if !existing.contains(where: { $0.identifier == domain.identifier }) {
                    try await NSFileProviderManager.add(domain)
                }
                guard let manager = NSFileProviderManager(for: domain) else {
                    throw CocoaError(.featureUnsupported)
                }
                for inode in delta.changedFileInodes {
                    try? await manager.evictItem(
                        identifier: NSFileProviderItemIdentifier("inode:\(inode)")
                    )
                }
                try await manager.signalEnumerator(for: .workingSet)
                try await manager.signalEnumerator(for: .rootContainer)
                domainURL = try await manager.getUserVisibleURL(for: .rootContainer)
                status = "Installed. Finder location: \(domainURL?.path ?? "File Provider")"
                NSLog("oxfs-fileprovider action=install status=success source=%@ domain=%@",
                      source.path, domainURL?.path ?? "unknown")
            } catch {
                status = "Install failed: \(error.localizedDescription)"
                NSLog("oxfs-fileprovider action=install status=error error=%@",
                      String(describing: error))
            }
        }
    }

    func installRemote() {
        let host = remoteHost.trimmingCharacters(in: .whitespacesAndNewlines)
        let selection = remoteSelection.trimmingCharacters(in: .whitespacesAndNewlines)
        Task {
            do {
                guard !selection.isEmpty else { throw RemoteError.invalidSelection }
                guard let container = FileManager.default.containerURL(
                    forSecurityApplicationGroupIdentifier: appGroup
                ) else {
                    throw CocoaError(.fileNoSuchFile)
                }
                status = "Resolving \(selection)…"
                let workspaceKey = "remote:\(host):\(selection)"
                let selectedMaxBytes = try resolvedCacheBytes(nil)
                let selectedPolicy = cachePolicy
                try RemoteSnapshotPublisher.publish(
                    host: host,
                    selection: selection,
                    workspaceKey: workspaceKey,
                    container: container,
                    cacheConfig: try cacheConfiguration(
                        maxBytes: selectedMaxBytes,
                        policy: selectedPolicy
                    )
                )
                let name = (selection as NSString).lastPathComponent
                let config = ProviderConfiguration(
                    sourceBookmark: Data(),
                    sourcePath: "/remote/\(host)/\(selection)",
                    sourceName: name,
                    deliveryMode: "staged",
                    workspaceKey: workspaceKey,
                    cacheMaxBytes: selectedMaxBytes,
                    cachePolicy: selectedPolicy
                )
                try JSONEncoder().encode(config).write(
                    to: container.appendingPathComponent("configuration.json"),
                    options: .atomic
                )
                let domain = NSFileProviderDomain(
                    identifier: domainIdentifier,
                    displayName: "oxFS — \(name)"
                )
                for oldDomain in try await NSFileProviderManager.domains()
                    where oldDomain.identifier == domain.identifier {
                    try await NSFileProviderManager.remove(oldDomain)
                }
                try await NSFileProviderManager.add(domain)
                guard let manager = NSFileProviderManager(for: domain) else {
                    throw CocoaError(.featureUnsupported)
                }
                try await manager.signalEnumerator(for: .workingSet)
                try await manager.signalEnumerator(for: .rootContainer)
                domainURL = try await manager.getUserVisibleURL(for: .rootContainer)
                status = "Remote selection installed: \(domainURL?.path ?? name)"
            } catch {
                let detail = String(describing: error)
                status = "Remote install failed: \(detail)"
                if let container = FileManager.default.containerURL(
                    forSecurityApplicationGroupIdentifier: appGroup
                ) {
                    try? Data(detail.utf8).write(
                        to: container.appendingPathComponent("remote-error.txt"),
                        options: .atomic
                    )
                }
            }
        }
    }

    private func resolvedCacheBytes(_ configured: UInt64?) throws -> UInt64 {
        if let configured, configured > 0 { return configured }
        guard let mib = UInt64(cacheMiB), mib > 0,
              !mib.multipliedReportingOverflow(by: 1024 * 1024).overflow else {
            throw CocoaError(.fileReadCorruptFile)
        }
        return mib * 1024 * 1024
    }

    private func cacheConfiguration(maxBytes: UInt64, policy: String) throws -> CacheConfig {
        let order: EvictionOrder
        switch policy {
        case "lrs": order = .leastRecentlySelectedGeneration
        case "clock": order = .clockSecondChance
        case "lfu": order = .approxLeastFrequentlyUsed
        default: throw CocoaError(.fileReadCorruptFile)
        }
        return CacheConfig(maxBytes: maxBytes, eviction: order)
    }

    func remove() {
        Task {
            do {
                for domain in try await NSFileProviderManager.domains()
                    where domain.identifier == domainIdentifier {
                    try await NSFileProviderManager.remove(domain)
                }
                domainURL = nil
                status = "Removed the oxFS File Provider domain."
            } catch {
                status = "Remove failed: \(error.localizedDescription)"
            }
        }
    }

    func reveal() {
        guard let domainURL else { return }
        NSWorkspace.shared.activateFileViewerSelecting([domainURL])
    }

    func refresh() {
        status = "Refreshing the selected source…"
        resumeExistingSelection(resetDomain: false)
    }

    func loadTelemetry() {
        guard let container = FileManager.default.containerURL(
            forSecurityApplicationGroupIdentifier: appGroup
        ),
        let data = try? Data(contentsOf: container.appendingPathComponent("telemetry.json"))
        else { return }
        telemetry = try? JSONDecoder().decode(ProviderTelemetry.self, from: data)
    }
}

struct ContentView: View {
    @StateObject private var model = AppModel()
    @State private var didAutoInstall = false

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("oxFS File Provider").font(.title)
            Text(model.status).textSelection(.enabled)
            if let telemetry = model.telemetry {
                Grid(alignment: .leading, horizontalSpacing: 18, verticalSpacing: 4) {
                    GridRow {
                        Text("Generation")
                        Text("\(telemetry.generation)")
                        Text("Visible files")
                        Text("\(telemetry.visibleFiles)")
                    }
                    GridRow {
                        Text("Resident")
                        Text("\(telemetry.residentObjects) objects / \(telemetry.residentBytes) bytes")
                        Text("Fetched")
                        Text("\(telemetry.fetchedObjects) objects / \(telemetry.fetchedBytes) bytes")
                    }
                    GridRow {
                        Text("Evicted / blocked")
                        Text("\(telemetry.evictedObjects) / \(telemetry.blockedEvictions)")
                        Text("FP fetches")
                        Text("\(telemetry.contentFetches) / \(telemetry.contentReadBytes) bytes")
                    }
                }
                .font(.system(.caption, design: .monospaced))
            }
            HStack {
                Button("Choose Folder & Install") { model.install() }
                Button("Reveal in Finder") { model.reveal() }
                    .disabled(model.domainURL == nil)
                Button("Refresh") { model.refresh() }
                Button("Remove Domain") { model.remove() }
            }
            HStack {
                Text("Cache")
                TextField("MiB", text: $model.cacheMiB)
                    .frame(width: 70)
                Text("MiB")
                Picker("Eviction", selection: $model.cachePolicy) {
                    Text("Least-recent selection").tag("lrs")
                    Text("CLOCK").tag("clock")
                    Text("Sampled LFU").tag("lfu")
                }
                .frame(width: 230)
            }
            Divider()
            HStack {
                TextField("Git host", text: $model.remoteHost)
                    .frame(width: 180)
                TextField("teams/team_id/path", text: $model.remoteSelection)
                Button("Publish Remote Selection") { model.installRemote() }
            }
        }
        .padding(24)
        .frame(minWidth: 620, minHeight: 170)
        .task {
            guard !didAutoInstall else { return }
            didAutoInstall = true
            let arguments = CommandLine.arguments
            if let selectionIndex = arguments.firstIndex(of: "--remote-selection"),
               arguments.indices.contains(selectionIndex + 1) {
                model.remoteSelection = arguments[selectionIndex + 1]
                if let hostIndex = arguments.firstIndex(of: "--remote-host"),
                   arguments.indices.contains(hostIndex + 1) {
                    model.remoteHost = arguments[hostIndex + 1]
                }
                model.installRemote()
                return
            }
            if arguments.contains("--activate-staged") {
                model.activateStagedWorkspace()
                return
            }
            if arguments.contains("--refresh-existing") {
                model.resumeExistingSelection(resetDomain: false)
                return
            }
            guard let index = arguments.firstIndex(of: "--source"),
                  arguments.indices.contains(index + 1) else {
                model.resumeExistingSelection()
                return
            }
            model.install(source: URL(fileURLWithPath: arguments[index + 1]), resetDomain: true)
        }
        .task {
            while !Task.isCancelled {
                model.loadTelemetry()
                try? await Task.sleep(for: .seconds(1))
            }
        }
    }
}

@main
struct OxfsFileProviderApp: App {
    var body: some Scene {
        WindowGroup {
            ContentView()
        }
    }
}
