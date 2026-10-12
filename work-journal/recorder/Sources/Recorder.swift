import Foundation
import AppKit
import ApplicationServices

/// Owns capture, mic, activity, socket and menu bar, and turns pause / exclusion / sleep /
/// disk-guard state and the audio mode (on / off / auto) into which writers run. Main thread only.
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
    private var audioMode: AudioMode
    private var auto = AutoAudio()
    private var autoTimer: Timer?
    /// Set by an auto start: the next `apply` flushes the buffered audio into the new chunks.
    private var pendingFlush = false
    private let listener = ConversationListener()

    init(options: RunOptions) {
        self.options = options
        log = ActivityLog(dataDir: options.dataDir)
        systemWriter = AudioChunkWriter(track: "system", dataDir: options.dataDir, chunkMinutes: options.chunkMinutes)
        micWriter = AudioChunkWriter(track: "mic", dataDir: options.dataDir, chunkMinutes: options.chunkMinutes)
        capture = Capture(options: options, systemWriter: systemWriter)
        mic = MicRecorder(writer: micWriter)
        monitor = ActivityMonitor(log: log, policy: options.policy, idleMinutes: options.idleMinutes)
        audioMode = options.audioMode
        let listener = self.listener
        mic.onPCM = { listener.feed($0, sampleRate: $1) }
    }

    private var state: String { paused ? "paused" : monitor.excluded ? "excluded" : "recording" }
    private var recordingAllowed: Bool { !paused && !sleeping }
    private var screensOn: Bool { recordingAllowed && !monitor.excluded && disk == .normal }
    /// Audio may run at all (the mode decides whether it does); during exclusion the writers
    /// stay open but store silence so the chunk timeline is intact.
    private var audioAllowed: Bool { recordingAllowed && disk != .audio_off }
    private var gate: AudioGate { audioGate(mode: audioMode, allowed: audioAllowed, autoRecording: auto.recording) }
    private var autoReason: AutoReason? {
        if case .recording(let r) = auto.phase { return r }
        return nil
    }

    /// `interactive` adds the menu bar and control socket (`run`); the selftest omits them.
    func start(interactive: Bool) throws {
        if interactive {
            let s = ControlServer(path: options.socket) { [unowned self] in handle($0) }
            try s.start()
            server = s
            menuBar = MenuBar(dashboardURL: options.dashboardURL,
                              onTogglePause: { [unowned self] in _ = paused ? resume() : pause() },
                              onAudioMode: { [unowned self] in setAudioMode($0) },
                              onQuit: { [unowned self] in quit() })
        }
        log.append("start")
        monitor.onExcludedChange = { [unowned self] _ in apply() }
        monitor.onMeetingChange = { [unowned self] _ in apply() }
        monitor.onSleep = { [unowned self] in sleeping = true; apply() }
        monitor.onWake = { [unowned self] in sleeping = false; apply() }
        monitor.onSwitch = { [unowned self] in capture.trigger(.appSwitch) }
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
        autoTimer = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { [unowned self] _ in autoTick() }
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
        // Pause, sleep, the disk guard or leaving auto end an auto recording and clear the buffer.
        if (!audioAllowed || audioMode != .auto) && auto.recording {
            auto.reset()
            if audioMode == .auto { log.append("audio_auto_stop") }
        }
        let g = gate
        let flush = pendingFlush && g == .write
        pendingFlush = false
        systemWriter.setGate(g, flush: flush)
        micWriter.setGate(g, flush: flush)
        listener.enabled = audioMode == .auto && g != .off
        if micWanted(g) { mic.start() } else { mic.stop() }
        monitor.logsApps = !paused
        updateMenu()
    }

    /// Auto mode, every 2 s: a call (another process records from the microphone, or a meeting
    /// app/title is in front) or a conversation heard by the mic starts recording, flushing the
    /// last 120 s; 180 s after both end it stops and listens again.
    private func autoTick() {
        guard audioMode == .auto, audioAllowed, !stopping else { return }
        let call = monitor.meeting || InputUsage.otherProcessRecording(ourEngineRunning: mic.isRunning)
        switch auto.step(now: Date(), call: call, conversation: listener.conversation) {
        case .start(let reason)?:
            log.append("audio_auto_start", reason: reason.rawValue)
            pendingFlush = true
            apply()
        case .stop?:
            log.append("audio_auto_stop")
            apply()
        case nil:
            if autoReason != lastMenuReason { updateMenu() }
        }
    }

    private var lastMenuReason: AutoReason?

    /// Switches the audio mode (socket or menu bar). Go reads the new mode from `status` and
    /// saves it as the default.
    private func setAudioMode(_ m: AudioMode) {
        guard m != audioMode else { return }
        if auto.recording && m != .on { log.append("audio_auto_stop") }
        auto.reset()
        listener.reset()
        audioMode = m
        log.append("audio_mode", mode: m.rawValue)
        apply()
    }

    private func updateMenu() {
        let degraded = options.degraded ? "Recorder restarted after errors" : nil
        lastMenuReason = autoReason
        menuBar?.update(paused: paused, error: captureError ?? micError ?? degraded, audioMode: audioMode, autoReason: autoReason)
    }

    private func report(_ message: String) {
        eprint("recorder: \(message)")
        updateMenu()
    }

    // MARK: Commands

    private func handle(_ command: String) -> (reply: String, then: (() -> Void)?) {
        if command.hasPrefix("audio_mode") {
            guard let m = parseAudioModeCommand(command) else {
                return ("error: audio_mode must be on, off or auto", nil)
            }
            setAudioMode(m)
            return ("ok", nil)
        }
        switch command {
        case "pause": pause()
        case "resume": resume()
        case "screens_off": setDisk(.screens_off)
        case "audio_off": setDisk(.audio_off)
        case "normal": setDisk(.normal)
        case "rotate": rotate()
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

    /// Finishes the open chunk of both tracks (blocks until written, so `ok` means the files
    /// are playable); the next samples open new chunks. Nothing to do while audio is off or
    /// only buffered.
    private func rotate() {
        systemWriter.rotate()
        micWriter.rotate()
    }

    /// `screens_off` stops screenshots; `audio_off` stops screenshots and audio; `normal` restores both.
    private func setDisk(_ d: Disk) {
        guard d != disk else { return }
        disk = d
        log.append(d.rawValue)
        apply()
    }

    private func statusJSON() -> String {
        let active = gate == .write && !monitor.excluded
        let obj: [String: Any] = ["state": state, "since": rfc3339(since), "meeting": monitor.meeting,
                                  "screens": screensOn, "audio": active, "audio_mode": audioMode.rawValue,
                                  "audio_active": active, "audio_reason": autoReason?.rawValue ?? ""]
        guard let data = try? JSONSerialization.data(withJSONObject: obj, options: [.sortedKeys]) else { return "{}" }
        return String(decoding: data, as: UTF8.self)
    }

    // MARK: Shutdown

    /// Finishes both writers (playable `.m4a`), stops capture, emits `stop`, then calls `done`.
    func shutdown(_ done: @escaping () -> Void) {
        guard !stopping else { return }
        stopping = true
        autoResume?.invalidate()
        autoTimer?.invalidate()
        systemWriter.setGate(.off, flush: false)
        micWriter.setGate(.off, flush: false)
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

/// Runs a non-interactive capture into `dataDir` for `seconds` (1 s gap and backstop), then
/// shuts down cleanly so the audio chunks are finished.
func runSelftestCapture(dataDir: URL, seconds: TimeInterval) throws {
    var options = RunOptions()
    options.dataDir = dataDir
    options.shots.minGap = 1
    options.shots.idleBackstop = 1
    options.shots.meetingInterval = 1
    let recorder = Recorder(options: options)
    try recorder.start(interactive: false)
    RunLoop.main.run(until: Date().addingTimeInterval(seconds))
    var done = false
    recorder.shutdown { done = true }
    while !done { RunLoop.main.run(until: Date().addingTimeInterval(0.1)) }
}
