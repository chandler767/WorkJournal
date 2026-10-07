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
