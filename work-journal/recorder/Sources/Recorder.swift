import Foundation
import AppKit
import ApplicationServices

/// Owns capture, mic, activity, socket and menu bar, and turns pause / exclusion / sleep /
/// disk-guard state into which writers run. Main thread only.
final class Recorder {
    private enum Disk: String { case normal, screens_off, audio_off }

    private let options: RunOptions
    private let log: ActivityLog
    private let systemWriter: AudioChunkWriter
    private let micWriter: AudioChunkWriter
    private let capture: Capture
    private let mic: MicRecorder
    private let monitor: ActivityMonitor
    private var menuBar: MenuBar?
    private var server: ControlServer?

    private var paused = false
    private var sleeping = false
    private var disk = Disk.normal
    private var lastState = "recording"
    private var since = Date()
    private var autoResume: Timer?
    private var captureError: String?
    private var micError: String?
    private var stopping = false

    init(options: RunOptions) {
        self.options = options
        log = ActivityLog(dataDir: options.dataDir)
        systemWriter = AudioChunkWriter(track: "system", dataDir: options.dataDir, chunkMinutes: options.chunkMinutes)
        micWriter = AudioChunkWriter(track: "mic", dataDir: options.dataDir, chunkMinutes: options.chunkMinutes)
        capture = Capture(options: options, systemWriter: systemWriter)
        mic = MicRecorder(writer: micWriter)
        monitor = ActivityMonitor(log: log, policy: options.policy, idleMinutes: options.idleMinutes)
    }

    private var state: String { paused ? "paused" : monitor.excluded ? "excluded" : "recording" }
    private var recordingAllowed: Bool { !paused && !sleeping }
    private var screensOn: Bool { recordingAllowed && !monitor.excluded && disk == .normal }
    /// Audio writers and mic run; during exclusion they stay open but store silence so the
    /// chunk timeline is intact.
    private var audioOn: Bool { recordingAllowed && disk != .audio_off }

    /// `interactive` adds the menu bar and control socket (`run`); the selftest omits them.
    func start(interactive: Bool) throws {
        if interactive {
            let s = ControlServer(path: options.socket) { [unowned self] in handle($0) }
            try s.start()
            server = s
            menuBar = MenuBar(dashboardURL: options.dashboardURL,
                              onTogglePause: { [unowned self] in _ = paused ? resume() : pause() },
                              onQuit: { [unowned self] in quit() })
        }
        log.append("start")
        monitor.onExcludedChange = { [unowned self] _ in apply() }
        monitor.onMeetingChange = { [unowned self] _ in apply() }
        monitor.onSleep = { [unowned self] in sleeping = true; apply() }
        monitor.onWake = { [unowned self] in sleeping = false; apply() }
        capture.onError = { [unowned self] msg in captureError = msg; report(msg) }
        capture.onRunning = { [unowned self] in captureError = nil; apply() }
        capture.isExcludedNow = { [unowned self] in monitor.refreshExclusion() }
        mic.onError = { [unowned self] msg in micError = msg; report(msg) }
        mic.onRunning = { [unowned self] in
            guard micError != nil else { return }
            micError = nil
            updateMenu()
        }
        monitor.start()
        capture.start()
        apply()
    }

    private func apply() {
        if state != lastState {
            lastState = state
            since = Date()
        }
        capture.meeting = monitor.meeting
        capture.screensEnabled = screensOn
        systemWriter.setMuted(monitor.excluded)
        micWriter.setMuted(monitor.excluded)
        systemWriter.setEnabled(audioOn)
        micWriter.setEnabled(audioOn)
        if audioOn { mic.start() } else { mic.stop() }
        monitor.logsApps = !paused
        updateMenu()
    }

    private func updateMenu() {
        let degraded = options.degraded ? "Recorder restarted after errors" : nil
        menuBar?.update(paused: paused, error: captureError ?? micError ?? degraded)
    }

    private func report(_ message: String) {
        eprint("recorder: \(message)")
        updateMenu()
    }

    // MARK: Commands

    private func handle(_ command: String) -> (reply: String, then: (() -> Void)?) {
        switch command {
        case "pause": pause()
        case "resume": resume()
        case "screens_off": setDisk(.screens_off)
        case "audio_off": setDisk(.audio_off)
        case "normal": setDisk(.normal)
        case "status": return (statusJSON(), nil)
        case "quit": return ("ok", { [unowned self] in quit() })
        default: return ("error: unknown command \"\(command)\"", nil)
        }
        return ("ok", nil)
    }

    private func pause() {
        guard !paused else { return }
        paused = true
        log.append("pause")
        autoResume = Timer.scheduledTimer(withTimeInterval: Double(options.pauseAutoResumeMinutes) * 60,
                                          repeats: false) { [unowned self] _ in resume() }
        apply()
    }

    private func resume() {
        guard paused else { return }
        autoResume?.invalidate()
        autoResume = nil
        paused = false
        log.append("resume")
        apply()
    }

    /// `screens_off` stops screenshots; `audio_off` stops screenshots and audio; `normal` restores both.
    private func setDisk(_ d: Disk) {
        guard d != disk else { return }
        disk = d
        log.append(d.rawValue)
        apply()
    }

    private func statusJSON() -> String {
        let obj: [String: Any] = ["state": state, "since": rfc3339(since), "meeting": monitor.meeting,
                                  "screens": screensOn, "audio": audioOn && !monitor.excluded]
        guard let data = try? JSONSerialization.data(withJSONObject: obj, options: [.sortedKeys]) else { return "{}" }
        return String(decoding: data, as: UTF8.self)
    }

    // MARK: Shutdown

    /// Finishes both writers (playable `.m4a`), stops capture, emits `stop`, then calls `done`.
    func shutdown(_ done: @escaping () -> Void) {
        guard !stopping else { return }
        stopping = true
        autoResume?.invalidate()
        systemWriter.setEnabled(false)
        micWriter.setEnabled(false)
        mic.stop()
        monitor.stop()
        server?.stop()
        var finished = false
        let finish = { [self] in
            guard !finished else { return }
            finished = true
            log.append("stop")
            done()
        }
        capture.stop(finish)
        DispatchQueue.main.asyncAfter(deadline: .now() + 3, execute: finish) // stopCapture may never call back
    }

    func quit() {
        shutdown { exit(0) }
    }
}

private var promptedForAccessibility = false

private func promptForAccessibilityIfNeeded(interactive: Bool) {
    guard shouldPromptForAccessibility(trusted: AXIsProcessTrusted(), interactive: interactive,
                                       alreadyPrompted: promptedForAccessibility) else { return }
    promptedForAccessibility = true
    let key = kAXTrustedCheckOptionPrompt.takeUnretainedValue() as String
    _ = AXIsProcessTrustedWithOptions([key: true] as CFDictionary)
}

/// `recorder run …`: menu bar app until `quit`, the Quit menu item, SIGTERM or SIGINT.
func runRecorder(args: [String]) -> Int32 {
    let options: RunOptions
    do {
        options = try RunOptions.parse(args)
        try FileManager.default.createDirectory(at: options.dataDir, withIntermediateDirectories: true)
    } catch {
        eprint("run: \(error.localizedDescription)")
        return 2
    }
    let app = NSApplication.shared
    app.setActivationPolicy(.accessory)
    promptForAccessibilityIfNeeded(interactive: true)
    let recorder = Recorder(options: options)
    do {
        try recorder.start(interactive: true)
    } catch is AnotherRecorderRunning {
        eprint("another recorder is running")
        return 3
    } catch {
        eprint("run: \(error.localizedDescription)")
        return 1
    }
    var signalSources: [DispatchSourceSignal] = []
    for sig in [SIGTERM, SIGINT] {
        signal(sig, SIG_IGN)
        let src = DispatchSource.makeSignalSource(signal: sig, queue: .main)
        src.setEventHandler { recorder.quit() }
        src.resume()
        signalSources.append(src)
    }
    app.run()
    withExtendedLifetime(signalSources) {}
    return 0
}

/// Runs a non-interactive capture into `dataDir` for `seconds` (1 s screenshot cadence), then
/// shuts down cleanly so the audio chunks are finished.
func runSelftestCapture(dataDir: URL, seconds: TimeInterval) throws {
    var options = RunOptions()
    options.dataDir = dataDir
    options.policy.interval = 1
    options.policy.meetingInterval = 1
    let recorder = Recorder(options: options)
    try recorder.start(interactive: false)
    RunLoop.main.run(until: Date().addingTimeInterval(seconds))
    var done = false
    recorder.shutdown { done = true }
    while !done { RunLoop.main.run(until: Date().addingTimeInterval(0.1)) }
}
