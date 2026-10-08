import Foundation

struct PolicyConfig {
    var excludeApps: [String] = []
    var excludeTitles: [String] = []
    var meetingApps: [String] = []
    var meetingTitles: [String] = []
    var interval: Int = 10
    var meetingInterval: Int = 5
}

private func containsFold(_ haystack: String, _ needle: String) -> Bool {
    !needle.isEmpty && haystack.range(of: needle, options: [.caseInsensitive, .diacriticInsensitive]) != nil
}

private func equalsFold(_ a: String, _ b: String) -> Bool {
    a.compare(b, options: [.caseInsensitive, .diacriticInsensitive]) == .orderedSame
}

/// True when the frontmost app/window must not be recorded.
func isExcluded(app: String, title: String, _ c: PolicyConfig) -> Bool {
    c.excludeApps.contains { equalsFold(app, $0) } || c.excludeTitles.contains { containsFold(title, $0) }
}

/// True when the app/window looks like a meeting (faster screenshot cadence).
func isMeeting(app: String, title: String, _ c: PolicyConfig) -> Bool {
    c.meetingApps.contains { equalsFold(app, $0) } || c.meetingTitles.contains { containsFold(title, $0) }
}

func screenshotInterval(meeting: Bool, _ c: PolicyConfig) -> Int {
    meeting ? c.meetingInterval : c.interval
}

/// "HHMM-<track>.m4a" in local time.
func chunkName(start: Date, track: String) -> String {
    let f = DateFormatter()
    f.locale = Locale(identifier: "en_US_POSIX")
    f.dateFormat = "HHmm"
    return "\(f.string(from: start))-\(track).m4a"
}

/// Compares two 16x16 grayscale thumbnails (256 bytes each); skips when the
/// mean absolute difference is below `threshold`. A missing or mismatched
/// previous frame never skips.
func shouldSkipScreenshot(prev: [UInt8]?, next: [UInt8], threshold: Double = 2.0) -> Bool {
    guard let prev = prev, prev.count == next.count, !next.isEmpty else { return false }
    var total = 0
    for i in 0..<next.count { total += abs(Int(prev[i]) - Int(next[i])) }
    return Double(total) / Double(next.count) < threshold
}

/// Chunk file name for a chunk starting at `start`: `HHMM-<track>.m4a`, or `HHMMSS-<track>.m4a`
/// when the HHMM name already exists for that day (`exists` is asked about bare file names).
func chunkFileName(start: Date, track: String, exists: (String) -> Bool) -> String {
    let name = chunkName(start: start, track: track)
    guard exists(name) else { return name }
    let f = DateFormatter()
    f.locale = Locale(identifier: "en_US_POSIX")
    f.dateFormat = "HHmmss"
    return "\(f.string(from: start))-\(track).m4a"
}

/// Whether to show the system Accessibility prompt: only for the interactive recorder,
/// only when not already trusted, and at most once per process. Prompting a trusted
/// process, or prompting from the selftest, just produces repeated dialogs.
func shouldPromptForAccessibility(trusted: Bool, interactive: Bool, alreadyPrompted: Bool) -> Bool {
    interactive && !trusted && !alreadyPrompted
}

/// Screenshot size per display: each display is scaled to `width` (keeping its aspect), so
/// with `--all-displays` the composite is `width` × displays wide.
func screenshotSizes(displays: [(width: Int, height: Int)], width: Int) -> [(width: Int, height: Int)] {
    displays.map { d in (max(1, width), max(1, width * d.height / max(1, d.width))) }
}

/// Size of the side-by-side composite: summed widths, tallest height.
func compositeSize(_ sizes: [(width: Int, height: Int)]) -> (width: Int, height: Int) {
    (sizes.reduce(0) { $0 + $1.width }, sizes.map(\.height).max() ?? 0)
}

/// Indices of display `frames` in arrangement order: left to right, then top to bottom.
func arrangementOrder(_ frames: [CGRect]) -> [Int] {
    frames.indices.sorted { a, b in
        let fa = frames[a], fb = frames[b]
        return fa.minX != fb.minX ? fa.minX < fb.minX : fa.minY < fb.minY
    }
}

/// Debounces capture restarts: display changes arrive in bursts, so a restart fires once,
/// `delay` after the last request.
struct RestartDebounce {
    let delay: TimeInterval
    private(set) var deadline: Date?

    init(delay: TimeInterval) { self.delay = delay }

    mutating func request(at now: Date) { deadline = now.addingTimeInterval(delay) }

    /// Seconds until the pending restart is due (0 when overdue), nil when none is pending.
    func wait(from now: Date) -> TimeInterval? { deadline.map { max(0, $0.timeIntervalSince(now)) } }

    /// True once `now` reaches the deadline; clears it so each burst fires once.
    mutating func fire(at now: Date) -> Bool {
        guard let d = deadline, now >= d else { return false }
        deadline = nil
        return true
    }
}

/// Delay before restarting after the stream stopped with an error (or every screenshot
/// failed): a quick 2 s re-query the first time (typically a display was connected or
/// removed), then the 30 s retry while the fast path keeps failing within a minute.
func stopErrorRestartDelay(now: Date, lastFast: Date?) -> TimeInterval {
    if let l = lastFast, now.timeIntervalSince(l) < 60 { return 30 }
    return 2
}
