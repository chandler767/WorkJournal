import Foundation
import AVFoundation
import CoreMedia

func runPolicyTests() -> Int32 {
    var c = PolicyConfig()
    c.excludeApps = ["1Password"]
    c.excludeTitles = ["Private Browsing"]
    c.meetingApps = ["zoom.us"]
    c.meetingTitles = ["Meet - ", "Huddle"]
    c.interval = 10
    c.meetingInterval = 5

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

    let checks: [(String, Bool)] = socketChecks() + rotateChecks() + restartChecks() + [
        ("excluded app 1Password", isExcluded(app: "1Password", title: "", c)),
        ("excluded app case-insensitive", isExcluded(app: "1password", title: "", c)),
        ("excluded title Private Browsing", isExcluded(app: "Safari", title: "Private Browsing — x", c)),
        ("not excluded Safari Docs", !isExcluded(app: "Safari", title: "Docs", c)),
        ("meeting zoom.us", isMeeting(app: "zoom.us", title: "Zoom Meeting", c)),
        ("meeting Chrome Meet", isMeeting(app: "Google Chrome", title: "Meet - abc-defg", c)),
        ("meeting Slack Huddle", isMeeting(app: "Slack", title: "Huddle with Priya", c)),
        ("not meeting Safari", !isMeeting(app: "Safari", title: "Docs", c)),
        ("meeting interval 5", screenshotInterval(meeting: true, c) == 5),
        ("normal interval 10", screenshotInterval(meeting: false, c) == 10),
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
