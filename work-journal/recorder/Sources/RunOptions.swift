import Foundation

/// Flags of `recorder run …` (see the Recorder CLI contract).
struct RunOptions {
    var dataDir = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent("WorkJournal")
    var socketPath: String?
    var chunkMinutes = 5
    var width = 1440
    var idleMinutes = 5
    var pauseAutoResumeMinutes = 60
    var dashboardURL = URL(string: "http://127.0.0.1:4747")!
    var allDisplays = false
    var degraded = false
    var audioMode = AudioMode.on
    /// Debug: auto mode's stop delay in seconds (default 180).
    var autoStopSeconds: Int?
    var policy = PolicyConfig()
    var shots = ShotConfig()

    var socket: String { socketPath ?? dataDir.appendingPathComponent("recorder.sock").path }

    static func parse(_ args: [String]) throws -> RunOptions {
        var o = RunOptions()
        var i = 0
        func value() throws -> String {
            guard i + 1 < args.count else { throw RunOptionsError("\(args[i]) needs a value") }
            i += 1
            return args[i]
        }
        func int() throws -> Int {
            let flag = args[i]
            guard let n = Int(try value()), n > 0 else { throw RunOptionsError("\(flag) needs a positive integer") }
            return n
        }
        var legacyInterval: Int?
        var backstopSet = false
        while i < args.count {
            switch args[i] {
            case "--data-dir": o.dataDir = URL(fileURLWithPath: try value())
            case "--socket": o.socketPath = try value()
            case "--interval": legacyInterval = try int() // superseded by --idle-backstop
            case "--meeting-interval": o.shots.meetingInterval = TimeInterval(try int())
            case "--min-gap": o.shots.minGap = TimeInterval(try int())
            case "--idle-backstop":
                o.shots.idleBackstop = TimeInterval(try int())
                backstopSet = true
            case "--change-sensitivity":
                let v = try value()
                guard let s = ChangeSensitivity(rawValue: v) else {
                    throw RunOptionsError("--change-sensitivity must be low, medium or high, got \(v)")
                }
                o.shots.sensitivity = s
            case "--capture-on-switch":
                switch try value() {
                case "true": o.shots.captureOnSwitch = true
                case "false": o.shots.captureOnSwitch = false
                case let v: throw RunOptionsError("--capture-on-switch must be true or false, got \(v)")
                }
            case "--chunk-minutes": o.chunkMinutes = try int()
            case "--width": o.width = try int()
            case "--idle-minutes": o.idleMinutes = try int()
            case "--pause-auto-resume": o.pauseAutoResumeMinutes = try int()
            case "--dashboard-url":
                let s = try value()
                guard let u = URL(string: s) else { throw RunOptionsError("bad --dashboard-url \(s)") }
                o.dashboardURL = u
            case "--all-displays": o.allDisplays = true
            case "--degraded": o.degraded = true
            case "--auto-stop-seconds": o.autoStopSeconds = try int()
            case "--audio-mode":
                let v = try value()
                guard let m = AudioMode(rawValue: v) else {
                    throw RunOptionsError("--audio-mode must be on, off or auto, got \(v)")
                }
                o.audioMode = m
            case "--exclude-app": o.policy.excludeApps.append(try value())
            case "--exclude-title": o.policy.excludeTitles.append(try value())
            case "--meeting-app": o.policy.meetingApps.append(try value())
            case "--meeting-title": o.policy.meetingTitles.append(try value())
            default: throw RunOptionsError("unknown flag \(args[i])")
            }
            i += 1
        }
        if !backstopSet, let n = legacyInterval { o.shots.idleBackstop = TimeInterval(n) }
        return o
    }
}

struct RunOptionsError: LocalizedError {
    let message: String
    init(_ m: String) { message = m }
    var errorDescription: String? { message }
}

private func localFormatter(_ format: String) -> DateFormatter {
    let f = DateFormatter()
    f.locale = Locale(identifier: "en_US_POSIX")
    f.timeZone = .current
    f.dateFormat = format
    return f
}

/// `<data-dir>/raw/YYYY-MM-DD` for the local day of `date`.
func rawDayDir(_ dataDir: URL, _ date: Date) -> URL {
    dataDir.appendingPathComponent("raw/\(localFormatter("yyyy-MM-dd").string(from: date))")
}

/// `HHMMSS.jpg` in local time.
func screenshotName(_ date: Date) -> String {
    localFormatter("HHmmss").string(from: date) + ".jpg"
}

/// RFC 3339 with the local offset, e.g. 2026-10-07T14:05:00+01:00.
func rfc3339(_ date: Date) -> String {
    let f = ISO8601DateFormatter()
    f.timeZone = .current
    return f.string(from: date)
}
