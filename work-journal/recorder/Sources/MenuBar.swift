import AppKit

/// Menu bar status item: icon `record.circle` (recording), `pause.circle` (paused) or
/// `exclamationmark.circle` (error / degraded), plus Pause/Resume, the audio mode
/// (Audio: On / Off / Auto, checked), Open Dashboard and Quit.
final class MenuBar: NSObject {
    private let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
    private let errorItem = NSMenuItem(title: "", action: nil, keyEquivalent: "")
    private let pauseItem = NSMenuItem(title: "Pause", action: #selector(togglePause), keyEquivalent: "")
    private let dashboardURL: URL
    private let onTogglePause: () -> Void
    private let onQuit: () -> Void
    private let onAudioMode: (AudioMode) -> Void
    private let audioItems: [(AudioMode, NSMenuItem)]

    init(dashboardURL: URL, onTogglePause: @escaping () -> Void, onAudioMode: @escaping (AudioMode) -> Void,
         onQuit: @escaping () -> Void) {
        self.dashboardURL = dashboardURL
        self.onTogglePause = onTogglePause
        self.onAudioMode = onAudioMode
        self.onQuit = onQuit
        audioItems = [AudioMode.on, .off, .auto].map { m in
            (m, NSMenuItem(title: "", action: #selector(pickAudioMode(_:)), keyEquivalent: ""))
        }
        super.init()
        let menu = NSMenu()
        errorItem.isEnabled = false
        errorItem.isHidden = true
        menu.autoenablesItems = false
        menu.addItem(errorItem)
        pauseItem.target = self
        menu.addItem(pauseItem)
        menu.addItem(.separator())
        for (m, item) in audioItems {
            item.target = self
            item.representedObject = m.rawValue
            menu.addItem(item)
        }
        menu.addItem(.separator())
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
    /// `autoReason` is set while auto mode is recording; `autoApp` names the call's app ("" if unknown).
    func update(paused: Bool, error: String?, audioMode: AudioMode, autoReason: AutoReason?, autoApp: String) {
        for (m, item) in audioItems {
            item.state = m == audioMode ? .on : .off
            switch m {
            case .on: item.title = "Audio: On"
            case .off: item.title = "Audio: Off"
            case .auto:
                let call = autoApp.isEmpty ? "call" : autoApp
                let detail = autoReason.map { $0 == .call ? " (recording: \(call))" : " (recording: in person)" }
                    ?? (audioMode == .auto ? " (listening)" : "")
                item.title = "Audio: Auto" + detail
            }
        }
        let symbol = paused ? "pause.circle" : (error != nil ? "exclamationmark.circle" : "record.circle")
        let label = paused ? "Work Journal paused" : (error != nil ? "Work Journal error" : "Work Journal recording")
        item.button?.image = NSImage(systemSymbolName: symbol, accessibilityDescription: label)
        item.button?.toolTip = label
        pauseItem.title = paused ? "Resume" : "Pause"
        errorItem.title = error ?? ""
        errorItem.isHidden = error == nil
    }

    @objc private func togglePause() { onTogglePause() }
    @objc private func pickAudioMode(_ sender: NSMenuItem) {
        if let raw = sender.representedObject as? String, let m = AudioMode(rawValue: raw) { onAudioMode(m) }
    }
    @objc private func openDashboard() { NSWorkspace.shared.open(dashboardURL) }
    @objc private func quit() { onQuit() }
}
