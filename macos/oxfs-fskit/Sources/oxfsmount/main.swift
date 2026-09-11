import Foundation
import OxfsFSKit

private func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data(("oxfsmount: \(message)\n").utf8))
    exit(2)
}

private var values: [String: String] = [:]
var args = Array(CommandLine.arguments.dropFirst())
while !args.isEmpty {
    let key = args.removeFirst()
    guard key.hasPrefix("--"), !args.isEmpty else { fail("missing value for \(key)") }
    values[key] = args.removeFirst()
}
guard let source = values["--source"], let mountpoint = values["--mountpoint"] else {
    fail("usage: oxfsmount --source DIR --mountpoint DIR [--state DIR] [--cache-bytes N]")
}
let cacheBytes = values["--cache-bytes"].flatMap(UInt64.init) ?? 1_073_741_824
let fm = FileManager.default
let sourceURL = URL(fileURLWithPath: source).standardizedFileURL
let defaultStateURL = fm.homeDirectoryForCurrentUser
    .appendingPathComponent("Library/Group Containers/group.ai.sageox.oxfs", isDirectory: true)
    .appendingPathComponent("oxfs-fskit-\(MountConfiguration.identifierUUID(for: sourceURL).uuidString.lowercased())", isDirectory: true)
let stateURL = values["--state"].map { URL(fileURLWithPath: $0).standardizedFileURL } ?? defaultStateURL
try fm.createDirectory(at: stateURL, withIntermediateDirectories: true)
try fm.createDirectory(atPath: mountpoint, withIntermediateDirectories: true)
let configURL = stateURL.appendingPathComponent(MountConfiguration.fileName)
try MountConfiguration(
    sourceBookmark: MountConfiguration.bookmark(for: sourceURL),
    cacheBytes: cacheBytes
).write(to: configURL)

let process = Process()
process.executableURL = URL(fileURLWithPath: "/sbin/mount")
process.arguments = ["-t", "oxfs", "-o", "rdonly", stateURL.path, mountpoint]
try process.run()
process.waitUntilExit()
guard process.terminationStatus == 0 else {
    fail("mount failed with status \(process.terminationStatus); verify the signed extension is installed and enabled")
}
print("mounted \(mountpoint) from \(sourceURL.path) state=\(stateURL.path)")
