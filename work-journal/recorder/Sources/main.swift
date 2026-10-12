import Foundation
import AVFoundation
import CoreMedia

func runPolicyTests() -> Int32 {
    var c = PolicyConfig()
    c.excludeApps = ["1Password"]
    c.excludeTitles = ["Private Browsing"]
    c.meetingApps = ["zoom.us"]
    c.meetingTitles = ["Meet - ", "Huddle"]

    var cal = Calendar(identifier: .gregorian)
    cal.timeZone = .current
    let t1405 = cal.date(from: DateComponents(year: 2026, month: 1, day: 2, hour: 14, minute: 5))!

    let t140530 = cal.date(from: DateComponents(year: 2026, month: 1, day: 2, hour: 14, minute: 5, second: 30))!
    let t1407 = cal.date(from: DateComponents(year: 2026, month: 1, day: 2, hour: 14, minute: 7, second: 30))!
    let t1410 = cal.date(from: DateComponents(year: 2026, month: 1, day: 2, hour: 14, minute: 10))!
    let t2358 = cal.date(from: DateComponents(year: 2026, month: 1, day: 2, hour: 23, minute: 58))!
    let midnight = cal.date(from: DateComponents(year: 2026, month: 1, day: 3))!

    let flat = [UInt8](repeating: 100, count: 256)
    let inverted = flat.map { 255 - $0 }

    let checks: [(String, Bool)] = socketChecks() + rotateChecks() + restartChecks() + shotChecks() + runOptionChecks() + audioModeChecks() + [
        ("excluded app 1Password", isExcluded(app: "1Password", title: "", c)),
        ("excluded app case-insensitive", isExcluded(app: "1password", title: "", c)),
        ("excluded title Private Browsing", isExcluded(app: "Safari", title: "Private Browsing — x", c)),
        ("not excluded Safari Docs", !isExcluded(app: "Safari", title: "Docs", c)),
        ("meeting zoom.us", isMeeting(app: "zoom.us", title: "Zoom Meeting", c)),
        ("meeting Chrome Meet", isMeeting(app: "Google Chrome", title: "Meet - abc-defg", c)),
        ("meeting Slack Huddle", isMeeting(app: "Slack", title: "Huddle with Priya", c)),
        ("not meeting Safari", !isMeeting(app: "Safari", title: "Docs", c)),
        ("chunk name mic", chunkName(start: t1405, track: "mic") == "1405-mic.m4a"),
        ("chunk ends on next 5-min boundary", chunkEnd(after: t1407, minutes: 5) == t1410),
        ("chunk starting on boundary ends 5 min later", chunkEnd(after: t1405, minutes: 5) == t1410),
        ("chunk never crosses midnight", chunkEnd(after: t2358, minutes: 7) == midnight),
        ("ax prompt when untrusted interactive", shouldPromptForAccessibility(trusted: false, interactive: true, alreadyPrompted: false)),
        ("no ax prompt when trusted", !shouldPromptForAccessibility(trusted: true, interactive: true, alreadyPrompted: false)),
        ("no ax prompt in selftest", !shouldPromptForAccessibility(trusted: false, interactive: false, alreadyPrompted: false)),
        ("ax prompt at most once", !shouldPromptForAccessibility(trusted: false, interactive: true, alreadyPrompted: true)),
        ("mic error reported first time", shouldReportMicError("microphone permission denied", last: nil)),
        ("same mic error not re-reported", !shouldReportMicError("microphone permission denied", last: "microphone permission denied")),
        ("new mic error reported", shouldReportMicError("cannot start microphone: x", last: "microphone permission denied")),
        ("chunk due at its end", chunkDue(now: t1410, end: t1410) && !chunkDue(now: t1407, end: t1410)),
        ("boundary finish delay", chunkFinishDelay(now: t1407, end: t1410) == 150 && chunkFinishDelay(now: t1410.addingTimeInterval(5), end: t1410) == 0),
        ("chunk file name free HHMM", chunkFileName(start: t140530, track: "mic", exists: { _ in false }) == "1405-mic.m4a"),
        ("chunk file name collision HHMMSS",
         chunkFileName(start: t140530, track: "mic", exists: { $0 == "1405-mic.m4a" }) == "140530-mic.m4a"),
        ("chunk file name collision checks own track",
         chunkFileName(start: t140530, track: "system", exists: { $0 == "1405-mic.m4a" }) == "1405-system.m4a"),
        ("chunk name system", chunkName(start: t1405, track: "system") == "1405-system.m4a"),
        ("identical thumbnails skipped", shouldSkipScreenshot(prev: flat, next: flat, threshold: 2.0)),
        ("inverted thumbnail not skipped", !shouldSkipScreenshot(prev: flat, next: inverted, threshold: 2.0)),
        ("first frame not skipped", !shouldSkipScreenshot(prev: nil, next: flat, threshold: 2.0)),
        ("single display keeps --width",
         screenshotSizes(displays: [(2880, 1800)], width: 1440).map { "\($0.width)x\($0.height)" } == ["1440x900"]),
        ("each display scaled to --width",
         screenshotSizes(displays: [(2880, 1800), (1920, 1080)], width: 1440).map { "\($0.width)x\($0.height)" }
            == ["1440x900", "1440x810"]),
        ("composite is width x displays, tallest height",
         compositeSize(screenshotSizes(displays: [(2880, 1800), (1920, 1080)], width: 1440)) == (2880, 900)),
        ("composite of one display",
         compositeSize(screenshotSizes(displays: [(1920, 1080)], width: 1000)) == (1000, 562)),
        ("displays ordered left to right",
         arrangementOrder([CGRect(x: 0, y: 0, width: 1440, height: 900), CGRect(x: -1920, y: 0, width: 1920, height: 1080),
                           CGRect(x: 1440, y: -200, width: 1000, height: 800)]) == [1, 0, 2]),
    ]
    var failed = 0
    for (name, ok) in checks where !ok {
        print("FAIL: \(name)")
        failed += 1
    }
    if failed > 0 { return 1 }
    print("run-tests ok (\(checks.count) checks)")
    return 0
}

/// Capture restart debounce (display changes come in bursts) and stop-error backoff.
func restartChecks() -> [(String, Bool)] {
    let t0 = Date(timeIntervalSince1970: 1_000_000)
    var d = RestartDebounce(delay: 2)
    let idle = d.wait(from: t0) == nil && !d.fire(at: t0)
    d.request(at: t0)
    d.request(at: t0.addingTimeInterval(1.5)) // a second notification in the burst pushes it back
    let notYet = !d.fire(at: t0.addingTimeInterval(3)) && d.wait(from: t0.addingTimeInterval(3)) == 0.5
    let fires = d.fire(at: t0.addingTimeInterval(3.5))
    let once = !d.fire(at: t0.addingTimeInterval(10)) && d.wait(from: t0.addingTimeInterval(10)) == nil
    return [
        ("restart debounce idle", idle),
        ("restart debounce waits for the burst to end", notYet),
        ("restart debounce fires after the delay", fires),
        ("restart debounce fires once", once),
        ("first stop error restarts fast", stopErrorRestartDelay(now: t0, lastFast: nil) == 2),
        ("stop error long after the last fast restart restarts fast",
         stopErrorRestartDelay(now: t0, lastFast: t0.addingTimeInterval(-61)) == 2),
        ("repeated stop errors back off to 30 s",
         stopErrorRestartDelay(now: t0, lastFast: t0.addingTimeInterval(-10)) == 30),
        ("screenshot failure refreshes displays only", recoveryAction(for: .screenshotsFailed) == .refreshDisplays),
        ("stream stop error restarts the stream", recoveryAction(for: .streamStopped) == .restartStream),
        ("screen change restarts the stream", recoveryAction(for: .screenParametersChanged) == .restartStream),
        ("stream restart wins over a pending refresh",
         mergeRecovery(.refreshDisplays, .restartStream) == .restartStream
            && mergeRecovery(.restartStream, .refreshDisplays) == .restartStream),
        ("first recovery is the requested one", mergeRecovery(nil, .refreshDisplays) == .refreshDisplays),
    ]
}

/// Change-driven screenshot scheduling: triggers, min gap, idle backstop, meeting backstop.
func shotChecks() -> [(String, Bool)] {
    let t0 = Date(timeIntervalSince1970: 2_000_000)
    let c = ShotConfig() // min gap 3, backstop 60, meeting 5, capture on switch
    func at(_ s: TimeInterval) -> Date { t0.addingTimeInterval(s) }
    func next(_ now: TimeInterval, _ pending: Set<ShotTrigger>, meeting: Bool = false, _ cfg: ShotConfig = c) -> ShotDecision {
        nextShot(now: at(now), lastShot: t0, pending: pending, meeting: meeting, cfg)
    }
    var noSwitch = c
    noSwitch.captureOnSwitch = false
    // A burst of content changes every 0.5 s right after a shot: one shot at the gap end.
    var last = t0, pending = Set<ShotTrigger>(), shots: [TimeInterval] = []
    for tick in stride(from: 0.5, through: 10, by: 0.5) {
        if tick <= 2 { pending.insert(.contentChange) }
        if nextShot(now: at(tick), lastShot: last, pending: pending, meeting: false, c) == .shootNow {
            shots.append(tick)
            last = at(tick)
            pending.removeAll()
        }
    }
    let flat = [UInt8](repeating: 100, count: 256)
    var nudged = flat
    for i in 0..<16 { nudged[i] = 140 } // mean diff 2.5
    return [
        ("first shot is immediate", nextShot(now: t0, lastShot: nil, pending: [], meeting: false, c) == .shootNow),
        ("app switch after the gap shoots now", next(10, [.appSwitch]) == .shootNow),
        ("app switch during the gap waits for the gap end", next(1, [.appSwitch]) == .at(at(3))),
        ("content change during the gap is deferred to the gap end", next(1, [.contentChange]) == .at(at(3))),
        ("content change at the gap end shoots", next(3, [.contentChange]) == .shootNow),
        ("burst of changes gives one shot at the gap end", shots == [3]),
        ("idle waits for the backstop", next(30, []) == .at(at(60))),
        ("idle backstop shoots", next(60, []) == .shootNow),
        ("meeting backstop is the meeting interval", next(1, [], meeting: true) == .at(at(5))),
        ("meeting keeps the min gap", next(1, [.contentChange], meeting: true) == .at(at(3))),
        ("capture-on-switch off ignores switches", next(10, [.appSwitch], noSwitch) == .at(at(60))),
        ("capture-on-switch off still shoots on change", next(10, [.contentChange], noSwitch) == .shootNow),
        ("resume trigger shoots after the gap", next(10, [.resumed], noSwitch) == .shootNow),
        ("backstop never shorter than the min gap",
         nextShot(now: at(1), lastShot: t0, pending: [], meeting: true,
                  ShotConfig(minGap: 8, idleBackstop: 60, meetingInterval: 5, captureOnSwitch: true, sensitivity: .medium))
            == .at(at(8))),
        ("sensitivity thresholds high < medium < low",
         ChangeSensitivity.high.threshold == 1 && ChangeSensitivity.medium.threshold == 3 && ChangeSensitivity.low.threshold == 8),
        ("dedup threshold never above the change threshold",
         [ChangeSensitivity.low, .medium, .high].allSatisfy { $0.dedupThreshold <= $0.threshold && $0.dedupThreshold <= 2 }),
        ("no reference frame: no change", !contentChanged(ref: nil, next: flat, threshold: 1)),
        ("same frame: no change", !contentChanged(ref: flat, next: flat, threshold: 1)),
        ("small change past high sensitivity", contentChanged(ref: flat, next: nudged, threshold: ChangeSensitivity.high.threshold)),
        ("small change below medium sensitivity", !contentChanged(ref: flat, next: nudged, threshold: ChangeSensitivity.medium.threshold)),
    ] + thumbChecks()
}

/// `grayThumb` on synthetic BGRA buffers: 16x16 cell means of luminance.
func thumbChecks() -> [(String, Bool)] {
    let w = 64, h = 32, bpr = w * 4 + 16 // padded rows
    var white = [UInt8](repeating: 255, count: bpr * h)
    let allWhite = white.withUnsafeBytes { grayThumb(bgra: $0.baseAddress!, width: w, height: h, bytesPerRow: bpr) }
    // Left half black.
    for y in 0..<h { for x in 0..<(w / 2) { for k in 0..<3 { white[y * bpr + x * 4 + k] = 0 } } }
    let half = white.withUnsafeBytes { grayThumb(bgra: $0.baseAddress!, width: w, height: h, bytesPerRow: bpr) }
    return [
        ("thumb is 16x16", allWhite.count == 256),
        ("white frame thumb is white", allWhite.allSatisfy { $0 >= 254 }),
        ("half-black frame: left cells dark, right cells light",
         (0..<16).allSatisfy { r in (0..<8).allSatisfy { half[r * 16 + $0] <= 1 } && (8..<16).allSatisfy { half[r * 16 + $0] >= 254 } }),
        ("tiny frame does not crash", [UInt8](repeating: 0, count: 8).withUnsafeBytes {
            grayThumb(bgra: $0.baseAddress!, width: 2, height: 1, bytesPerRow: 8).count == 256 }),
    ]
}

/// `recorder run` flags for change-driven capture.
func runOptionChecks() -> [(String, Bool)] {
    let d = (try? RunOptions.parse([]))?.shots
    let set = (try? RunOptions.parse(["--min-gap", "5", "--idle-backstop", "120", "--change-sensitivity", "high",
                                      "--capture-on-switch", "false", "--meeting-interval", "4"]))?.shots
    let legacy = (try? RunOptions.parse(["--interval", "10"]))?.shots
    let both = (try? RunOptions.parse(["--interval", "10", "--idle-backstop", "90"]))?.shots
    func fails(_ a: [String]) -> Bool { (try? RunOptions.parse(a)) == nil }
    return [
        ("run defaults: gap 3, backstop 60, medium, switch on, meeting 5",
         d.map { $0.minGap == 3 && $0.idleBackstop == 60 && $0.sensitivity == .medium && $0.captureOnSwitch && $0.meetingInterval == 5 } == true),
        ("run flags parsed",
         set.map { $0.minGap == 5 && $0.idleBackstop == 120 && $0.sensitivity == .high && !$0.captureOnSwitch && $0.meetingInterval == 4 } == true),
        ("--interval maps to the backstop when --idle-backstop is absent", legacy?.idleBackstop == 10),
        ("--idle-backstop wins over --interval", both?.idleBackstop == 90),
        ("bad sensitivity rejected", fails(["--change-sensitivity", "max"])),
        ("bad capture-on-switch rejected", fails(["--capture-on-switch", "maybe"])),
        ("capture-on-switch true accepted", (try? RunOptions.parse(["--capture-on-switch", "true"]))?.shots.captureOnSwitch == true),
        ("zero min gap rejected", fails(["--min-gap", "0"])),
    ]
}

/// Control-socket ownership: a live listener is detected and never unlinked; a stale socket
/// file is. Uses a temp dir; the probe connections are never answered (main is busy here).
func socketChecks() -> [(String, Bool)] {
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent("wjrt-\(getpid())")
    try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: dir) }
    let path = dir.appendingPathComponent("r.sock").path
    var out: [(String, Bool)] = [("no socket file: no listener", !socketHasListener(path: path))]
    let first = ControlServer(path: path) { _ in ("ok", nil) }
    guard (try? first.start()) != nil else { return out + [("first control server starts", false)] }
    out.append(("live socket has a listener", socketHasListener(path: path)))
    var refused = false
    do { try ControlServer(path: path) { _ in ("ok", nil) }.start() } catch is AnotherRecorderRunning { refused = true } catch {}
    out.append(("second control server refuses a live socket", refused))
    out.append(("refused start leaves the live socket in place", socketHasListener(path: path)))
    first.stop()
    out.append(("stopped server: no listener", !socketHasListener(path: path)))
    // A stale socket file (bound, never listened on, not unlinked) is replaced.
    let s = socket(AF_UNIX, SOCK_STREAM, 0)
    var addr = sockaddr_un()
    addr.sun_family = sa_family_t(AF_UNIX)
    withUnsafeMutableBytes(of: &addr.sun_path) { buf in
        let b = Array(path.utf8)
        buf.copyBytes(from: b)
        buf[b.count] = 0
    }
    _ = withUnsafePointer(to: &addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(s, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
    }
    close(s)
    out.append(("stale socket: no listener", FileManager.default.fileExists(atPath: path) && !socketHasListener(path: path)))
    let third = ControlServer(path: path) { _ in ("ok", nil) }
    out.append(("stale socket is replaced", (try? third.start()) != nil && socketHasListener(path: path)))
    third.stop()
    return out
}

/// `AudioChunkWriter.rotate()` finishes the open chunk (playable) and the next sample opens a
/// new one named HHMMSS (HHMM is taken). Uses synthetic silent LPCM; no permissions needed.
func rotateChecks() -> [(String, Bool)] {
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent("wjrot-\(getpid())")
    try? FileManager.default.removeItem(at: dir)
    defer { try? FileManager.default.removeItem(at: dir) }
    let w = AudioChunkWriter(track: "test", dataDir: dir, chunkMinutes: 60)
    var pts: Int64 = 0
    func feed(_ n: Int) {
        for _ in 0..<n {
            if let s = silentSample(frames: 4800, at: pts) { w.append(s) }
            pts += 4800
        }
    }
    let audio = rawDayDir(dir, Date()).appendingPathComponent("audio")
    func chunks() -> [URL] {
        ((try? FileManager.default.contentsOfDirectory(at: audio, includingPropertiesForKeys: nil)) ?? [])
            .filter { $0.lastPathComponent.hasSuffix("-test.m4a") }.sorted { $0.path < $1.path }
    }
    func playable(_ u: URL) -> Bool {
        guard let f = try? AVAudioFile(forReading: u) else { return false }
        return f.length > 0
    }
    w.rotate() // nothing open yet: no-op
    let noneBefore = chunks().isEmpty
    feed(10)
    w.rotate()
    // Checked right after rotate() returns: the file must already be complete.
    let afterFirst = chunks()
    let firstPlayable = afterFirst.count == 1 && afterFirst.allSatisfy(playable)
    feed(10)
    w.rotate()
    let afterSecond = chunks()
    let secondPlayable = afterSecond.count == 2 && afterSecond.allSatisfy(playable)
    w.finish()
    return [
        ("rotate with no open chunk writes nothing", noneBefore),
        ("rotate finishes a playable chunk", firstPlayable),
        ("next sample after rotate opens a new chunk", secondPlayable),
        ("rotated chunk named HHMMSS on collision", afterSecond.contains { $0.lastPathComponent.count == "HHMMSS-test.m4a".count }),
    ]
}

/// A ready mono Float32 48 kHz LPCM sample buffer of zeros at `at` (in 1/48000 s).
private func silentSample(frames: Int, at: Int64) -> CMSampleBuffer? {
    guard let fmt = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 48_000, channels: 1, interleaved: false),
          let pcm = AVAudioPCMBuffer(pcmFormat: fmt, frameCapacity: AVAudioFrameCount(frames)) else { return nil }
    pcm.frameLength = AVAudioFrameCount(frames)
    var desc: CMAudioFormatDescription?
    guard CMAudioFormatDescriptionCreate(allocator: nil, asbd: fmt.streamDescription, layoutSize: 0, layout: nil,
                                         magicCookieSize: 0, magicCookie: nil, extensions: nil,
                                         formatDescriptionOut: &desc) == noErr else { return nil }
    var timing = CMSampleTimingInfo(duration: CMTime(value: 1, timescale: 48_000),
                                    presentationTimeStamp: CMTime(value: at, timescale: 48_000), decodeTimeStamp: .invalid)
    var sample: CMSampleBuffer?
    guard CMSampleBufferCreate(allocator: nil, dataBuffer: nil, dataReady: false, makeDataReadyCallback: nil, refcon: nil,
                               formatDescription: desc, sampleCount: CMItemCount(frames), sampleTimingEntryCount: 1,
                               sampleTimingArray: &timing, sampleSizeEntryCount: 0, sampleSizeArray: nil,
                               sampleBufferOut: &sample) == noErr, let sb = sample,
          CMSampleBufferSetDataBufferFromAudioBufferList(sb, blockBufferAllocator: nil, blockBufferMemoryAllocator: nil,
                                                         flags: 0, bufferList: pcm.audioBufferList) == noErr
    else { return nil }
    return sb
}

let args = Array(CommandLine.arguments.dropFirst())
switch args.first {
case "run-tests":
    exit(runPolicyTests())
case "ocr":
    exit(runOCR(Array(args.dropFirst())))
case "selftest":
    exit(runSelfTest(args: Array(args.dropFirst())))
case "run":
    exit(runRecorder(args: Array(args.dropFirst())))
default:
    eprint("usage: recorder run|ocr|selftest|run-tests")
    exit(2)
}
