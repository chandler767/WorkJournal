import Foundation

struct PolicyConfig {
    var excludeApps: [String] = []
    var excludeTitles: [String] = []
    var meetingApps: [String] = []
    var meetingTitles: [String] = []
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

/// How much the screen must change (mean absolute difference of 16x16 grayscale thumbnails,
/// 0–255) before a screenshot is taken. Typing a line or scrolling a little is about 1–2;
/// switching a browser tab or document is well above 8.
enum ChangeSensitivity: String {
    case low, medium, high

    var threshold: Double {
        switch self {
        case .high: return 1.0
        case .medium: return 3.0
        case .low: return 8.0
        }
    }

    /// Near-duplicate threshold for the full screenshot against the last one written: never
    /// above the change threshold, or a change-triggered shot could be dropped as unchanged.
    var dedupThreshold: Double { min(2.0, threshold / 2) }
}

/// Change-driven screenshot settings (`--min-gap`, `--idle-backstop`, `--meeting-interval`,
/// `--capture-on-switch`, `--change-sensitivity`); times in seconds.
struct ShotConfig {
    var minGap: TimeInterval = 3
    var idleBackstop: TimeInterval = 60
    var meetingInterval: TimeInterval = 5
    var captureOnSwitch = true
    var sensitivity = ChangeSensitivity.medium
}

/// Why a screenshot is wanted besides the idle backstop.
enum ShotTrigger: Hashable {
    case appSwitch     // frontmost app or window title changed
    case contentChange // the low-res watcher saw the screen change
    case resumed       // screens re-enabled (resume, exclusion ended, displays refreshed)
}

enum ShotDecision: Equatable {
    case shootNow
    case at(Date) // re-evaluate (and normally shoot) then
}

/// When to take the next screenshot. With no previous shot: now. Pending triggers fire at
/// `lastShot + minGap` at the earliest (so a burst coalesces into one shot at the gap end);
/// with none, the backstop fires `idleBackstop` (in a meeting at most `meetingInterval`, never
/// under `minGap`) after the last shot. App switches count only with `captureOnSwitch`.
func nextShot(now: Date, lastShot: Date?, pending: Set<ShotTrigger>, meeting: Bool, _ c: ShotConfig) -> ShotDecision {
    guard let last = lastShot else { return .shootNow }
    let live = c.captureOnSwitch ? pending : pending.subtracting([.appSwitch])
    let backstop = max(c.minGap, meeting ? min(c.idleBackstop, c.meetingInterval) : c.idleBackstop)
    let due = last.addingTimeInterval(live.isEmpty ? backstop : c.minGap)
    return due <= now ? .shootNow : .at(due)
}

/// True when the watcher frame moved past `threshold` from the reference frame (the frame at
/// the last trigger). No reference yet is not a change.
func contentChanged(ref: [UInt8]?, next: [UInt8], threshold: Double) -> Bool {
    ref != nil && !shouldSkipScreenshot(prev: ref, next: next, threshold: threshold)
}

/// 16x16 grayscale thumbnail (row 0 at the top) of a 32-bit BGRA frame: each cell is the mean
/// luminance of its pixels. Frames smaller than 16 px map several cells to one pixel.
func grayThumb(bgra base: UnsafeRawPointer, width: Int, height: Int, bytesPerRow: Int) -> [UInt8] {
    var out = [UInt8](repeating: 0, count: 256)
    guard width > 0, height > 0 else { return out }
    let p = base.assumingMemoryBound(to: UInt8.self)
    for cy in 0..<16 {
        let y0 = cy * height / 16, y1 = max(y0 + 1, (cy + 1) * height / 16)
        for cx in 0..<16 {
            let x0 = cx * width / 16, x1 = max(x0 + 1, (cx + 1) * width / 16)
            var sum = 0
            for y in y0..<min(y1, height) {
                let row = p + y * bytesPerRow
                for x in x0..<min(x1, width) {
                    let px = row + x * 4 // B, G, R, A
                    sum += 29 * Int(px[0]) + 150 * Int(px[1]) + 77 * Int(px[2]) // ≈ Rec.601 luma × 256
                }
            }
            let n = (min(y1, height) - y0) * (min(x1, width) - x0)
            out[cy * 16 + cx] = UInt8(min(255, sum / max(1, n) / 256))
        }
    }
    return out
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

/// Why capture needs recovering.
enum CaptureFailure { case screenshotsFailed, streamStopped, screenParametersChanged }

/// How to recover: refresh only the screenshot display list (keeps the audio stream
/// running), or stop and restart the whole stream.
enum RecoveryAction { case refreshDisplays, restartStream }

/// Screenshot-only failures (e.g. a locked screen, a stale display) re-query the displays
/// without touching audio; the stream's own stop error or a display change restarts it.
func recoveryAction(for f: CaptureFailure) -> RecoveryAction {
    f == .screenshotsFailed ? .refreshDisplays : .restartStream
}

/// Combines a pending recovery with a new request: a stream restart covers a refresh.
func mergeRecovery(_ pending: RecoveryAction?, _ next: RecoveryAction) -> RecoveryAction {
    pending == .restartStream || next == .restartStream ? .restartStream : .refreshDisplays
}
