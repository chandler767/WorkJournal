import AppKit

/// Menu bar status item: icon `record.circle` (recording), `pause.circle` (paused) or
/// `exclamationmark.circle` (error / degraded), plus Pause/Resume, Open Dashboard and Quit.
final class MenuBar: NSObject {
    private let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
    private let errorItem = NSMenuItem(title: "", action: nil, keyEquivalent: "")
    private let pauseItem = NSMenuItem(title: "Pause", action: #selector(togglePause), keyEquivalent: "")
    private let dashboardURL: URL
    private let onTogglePause: () -> Void
    private let onQuit: () -> Void

    init(dashboardURL: URL, onTogglePause: @escaping () -> Void, onQuit: @escaping () -> Void) {
        self.dashboardURL = dashboardURL
        self.onTogglePause = onTogglePause
        self.onQuit = onQuit
        super.init()
        let menu = NSMenu()
        errorItem.isEnabled = false
        errorItem.isHidden = true
        menu.autoenablesItems = false
        menu.addItem(errorItem)
        pauseItem.target = self
        menu.addItem(pauseItem)
        let open = NSMenuItem(title: "Open Dashboard", action: #selector(openDashboard), keyEquivalent: "")
        open.target = self
        menu.addItem(open)
        menu.addItem(.separator())
        let quit = NSMenuItem(title: "Quit", action: #selector(quit), keyEquivalent: "q")
        quit.target = self
        menu.addItem(quit)
        item.menu = menu
    }

    /// `error` is a short message shown as a disabled item; nil hides it.
    func update(paused: Bool, error: String?) {
        let symbol = paused ? "pause.circle" : (error != nil ? "exclamationmark.circle" : "record.circle")
        let label = paused ? "Work Journal paused" : (error != nil ? "Work Journal error" : "Work Journal recording")
        item.button?.image = NSImage(systemSymbolName: symbol, accessibilityDescription: label)
        item.button?.toolTip = label
        pauseItem.title = paused ? "Resume" : "Pause"
        errorItem.title = error ?? ""
        errorItem.isHidden = error == nil
    }

    @objc private func togglePause() { onTogglePause() }
    @objc private func openDashboard() { NSWorkspace.shared.open(dashboardURL) }
    @objc private func quit() { onQuit() }
}
