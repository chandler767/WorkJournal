import Foundation
import AppKit
import ApplicationServices

/// Appends contract events to `raw/<day>/activity.jsonl`.
final class ActivityLog {
    private struct Event: Encodable {
        let ts: String
        let type: String
        let app: String?
        let bundle: String?
        let title: String?
    }

    private let dataDir: URL
    private let lock = NSLock()
    private let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        return e
    }()

    init(dataDir: URL) { self.dataDir = dataDir }

    /// `app`/`bundle`/`title` are only passed for `app` events.
    func append(_ type: String, app: String? = nil, bundle: String? = nil, title: String? = nil) {
        let now = Date()
        lock.lock()
        defer { lock.unlock() }
        do {
            var line = try encoder.encode(Event(ts: rfc3339(now), type: type, app: app, bundle: bundle, title: title))
            line.append(0x0A)
            let dir = rawDayDir(dataDir, now)
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            let file = dir.appendingPathComponent("activity.jsonl")
            if !FileManager.default.fileExists(atPath: file.path) {
                FileManager.default.createFile(atPath: file.path, contents: nil)
            }
            let h = try FileHandle(forWritingTo: file)
            defer { try? h.close() }
            try h.seekToEnd()
            try h.write(contentsOf: line)
        } catch {
            eprint("activity: cannot write \(type) event: \(error.localizedDescription)")
        }
    }
}

/// Watches the frontmost app/window, idle time and sleep/wake (main thread only).
/// Logs `app`, `idle`, `active`, `sleep`, `wake`, `excluded` and `included`; the
/// recorder reacts to exclusion, meeting and sleep changes through the callbacks.
final class ActivityMonitor {
    private struct Frontmost: Equatable {
        var app = "", bundle = "", title = ""
    }

    var onExcludedChange: ((Bool) -> Void)?
    var onMeetingChange: ((Bool) -> Void)?
    var onSleep: (() -> Void)?
    var onWake: (() -> Void)?
    /// The frontmost app or its focused window title changed (not fired for the first poll).
    var onSwitch: (() -> Void)?

    private(set) var excluded = false
    private(set) var meeting = false
    /// False while paused: `app` events are suppressed, and the current app is logged on resume.
    var logsApps = true {
        didSet { if logsApps && !oldValue { lastLogged = nil; poll() } }
    }

    private let log: ActivityLog
    private let policy: PolicyConfig
    private let idleSeconds: Double
    private var idle = false
    private var lastLogged: Frontmost?
    private var lastSeen: Frontmost?
    private var timer: Timer?
    private var observers: [NSObjectProtocol] = []

    init(log: ActivityLog, policy: PolicyConfig, idleMinutes: Int) {
        self.log = log
        self.policy = policy
        self.idleSeconds = Double(idleMinutes) * 60
    }

    func start() {
        let nc = NSWorkspace.shared.notificationCenter
        observers = [
            nc.addObserver(forName: NSWorkspace.didActivateApplicationNotification, object: nil, queue: .main) { [weak self] _ in
                self?.poll()
            },
            nc.addObserver(forName: NSWorkspace.willSleepNotification, object: nil, queue: .main) { [weak self] _ in
                self?.log.append("sleep")
                self?.onSleep?()
            },
            nc.addObserver(forName: NSWorkspace.didWakeNotification, object: nil, queue: .main) { [weak self] _ in
                self?.log.append("wake")
                self?.onWake?()
            },
        ]
        timer = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { [weak self] _ in self?.poll() }
        poll()
    }

    func stop() {
        timer?.invalidate()
        timer = nil
        observers.forEach { NSWorkspace.shared.notificationCenter.removeObserver($0) }
        observers = []
    }

    /// Re-checks the frontmost app and window title right now (e.g. just before a screenshot),
    /// firing any exclusion transition immediately; returns whether recording is excluded.
    @discardableResult
    func refreshExclusion() -> Bool {
        poll()
        return excluded
    }

    private func poll() {
        let f = Self.frontmost()
        let nowExcluded = isExcluded(app: f.app, title: f.title, policy)
        if nowExcluded != excluded {
            excluded = nowExcluded
            log.append(nowExcluded ? "excluded" : "included")
            lastLogged = nil
            onExcludedChange?(nowExcluded)
        }
        if !excluded && logsApps && f != lastLogged {
            log.append("app", app: f.app, bundle: f.bundle, title: f.title)
            lastLogged = f
        }
        let nowMeeting = !excluded && isMeeting(app: f.app, title: f.title, policy)
        if nowMeeting != meeting {
            meeting = nowMeeting
            onMeetingChange?(nowMeeting)
        }
        if f != lastSeen {
            let first = lastSeen == nil
            lastSeen = f
            if !first { onSwitch?() }
        }
        pollIdle()
    }

    private func pollIdle() {
        // `.null` (0) reports time since boot here, not since the last input; ~0 is
        // kCGAnyInputEventType, which is what "any input" means.
        let anyInput = CGEventType(rawValue: ~0)!
        let secs = CGEventSource.secondsSinceLastEventType(.combinedSessionState, eventType: anyInput)
        let nowIdle = secs >= idleSeconds
        if nowIdle != idle {
            idle = nowIdle
            log.append(nowIdle ? "idle" : "active")
        }
    }

    private static func frontmost() -> Frontmost {
        guard let app = NSWorkspace.shared.frontmostApplication else { return Frontmost() }
        return Frontmost(app: app.localizedName ?? "", bundle: app.bundleIdentifier ?? "",
                         title: focusedWindowTitle(pid: app.processIdentifier))
    }

    /// Needs Accessibility permission; returns "" without it.
    private static func focusedWindowTitle(pid: pid_t) -> String {
        guard AXIsProcessTrusted() else { return "" } // untrusted AX calls can re-trigger the system prompt
        let axApp = AXUIElementCreateApplication(pid)
        var window: CFTypeRef?
        guard AXUIElementCopyAttributeValue(axApp, kAXFocusedWindowAttribute as CFString, &window) == .success,
              let win = window, CFGetTypeID(win) == AXUIElementGetTypeID() else { return "" }
        var title: CFTypeRef?
        guard AXUIElementCopyAttributeValue(win as! AXUIElement, kAXTitleAttribute as CFString, &title) == .success else {
            return ""
        }
        return (title as? String) ?? ""
    }
}
