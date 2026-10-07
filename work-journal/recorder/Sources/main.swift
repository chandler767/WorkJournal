import Foundation

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

    let checks: [(String, Bool)] = socketChecks() + [
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
