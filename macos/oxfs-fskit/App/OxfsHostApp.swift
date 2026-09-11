import AppKit

@main
@MainActor
final class OxfsHostApp: NSObject, NSApplicationDelegate {
    private let window = NSWindow(
        contentRect: NSRect(x: 0, y: 0, width: 520, height: 180),
        styleMask: [.titled, .closable, .miniaturizable],
        backing: .buffered, defer: false
    )

    static func main() {
        let app = NSApplication.shared
        let delegate = OxfsHostApp()
        app.delegate = delegate
        app.run()
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        let label = NSTextField(labelWithString:
            "oxFS installs a read-only FSKit filesystem extension.\nEnable it in System Settings → General → Login Items & Extensions."
        )
        label.alignment = .center
        label.frame = NSRect(x: 30, y: 55, width: 460, height: 60)
        window.contentView?.addSubview(label)
        window.title = "oxFS"
        window.center()
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }
}
