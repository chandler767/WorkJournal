import Foundation
import AppKit
import AVFoundation
import CoreAudio
import CoreMedia

// Audio recording modes (`--audio-mode`, socket `audio_mode on|off|auto`, menu bar). They gate
// the microphone and system audio only; screenshots are unaffected, and pause/sleep/the disk
// guard still stop everything.
//
// - on: today's behavior, audio is always written.
// - off: the mic engine is stopped and system audio samples are ignored.
// - auto: the mic runs but nothing is written. Both tracks keep the last 120 s in memory
//   (`AudioRing`). When a call (another process uses the microphone, or a meeting app/title is
//   in front) or an in-person conversation (`ConversationDetector`) is detected, the buffers are
//   flushed into new chunks and recording continues live; 180 s after both signals end it
//   stops and goes back to listening. Nothing buffered is ever written unless a start fires.

enum AudioMode: String {
    case on, off, auto
}

/// `audio_mode on|off|auto` → the mode; nil for anything else.
func parseAudioModeCommand(_ command: String) -> AudioMode? {
    let parts = command.split(separator: " ")
    guard parts.count == 2, parts[0] == "audio_mode" else { return nil }
    return AudioMode(rawValue: String(parts[1]))
}

/// What the audio writers do with incoming samples.
enum AudioGate {
    case off    // drop (finishes the open chunk, clears the buffer)
    case buffer // keep the last `AudioRing.seconds` in memory only
    case write  // write chunks
}

/// The writers' gate for `mode`. `allowed` is false while paused, asleep or under the disk
/// guard's audio_off; `autoRecording` is whether auto mode detected a call or conversation.
func audioGate(mode: AudioMode, allowed: Bool, autoRecording: Bool) -> AudioGate {
    guard allowed else { return .off }
    switch mode {
    case .on: return .write
    case .off: return .off
    case .auto: return autoRecording ? .write : .buffer
    }
}

/// The microphone runs whenever its samples are wanted (written or buffered).
func micWanted(_ gate: AudioGate) -> Bool { gate != .off }

// MARK: Voice activity

/// Voice activity thresholds (30 ms frames; RMS of samples in [-1, 1]).
enum VAD {
    static let frameSeconds = 0.030
    /// Absolute floor: quieter frames are never speech (≈ -44 dBFS).
    static let minRMS: Float = 0.006
    /// Speech must be this many times the tracked background level.
    static let noiseFactor: Float = 3.0
    /// The background level is the quietest frame of the last `noiseWindowSeconds`: it drops at
    /// once to a quieter frame and follows a room that gets louder within the window. Speech
    /// has gaps between words, so talking does not raise it.
    static let noiseWindowSeconds = 5.0
    /// Lowest background level, so digital silence (RMS 0) cannot pin it at zero.
    static let noiseFloorEpsilon: Float = 1e-4
    /// Zero-crossing rate (crossings per sample) above which a frame is hiss or noise rather
    /// than voiced speech (a voice is typically under 0.15; white noise is about 0.5).
    static let maxZCR: Float = 0.35
}

func frameRMS(_ x: UnsafeBufferPointer<Float>) -> Float {
    guard !x.isEmpty else { return 0 }
    var sum: Float = 0
    for v in x { sum += v * v }
    return (sum / Float(x.count)).squareRoot()
}

func zeroCrossingRate(_ x: UnsafeBufferPointer<Float>) -> Float {
    guard x.count > 1 else { return 0 }
    var n = 0
    for i in 1..<x.count where (x[i - 1] >= 0) != (x[i] >= 0) { n += 1 }
    return Float(n) / Float(x.count - 1)
}

/// Energy / zero-crossing voice activity detector with a tracked background level. `feed`
/// cuts arbitrary buffers into 30 ms frames (carrying the remainder over).
struct VoiceActivityDetector {
    private(set) var noiseFloor: Float = VAD.minRMS
    private var pending: [Float] = []
    private var recent: [Float] = [] // frame RMS of the last noise window (ring)
    private var recentHead = 0

    /// Classifies one frame and updates the background level.
    mutating func isSpeech(_ frame: UnsafeBufferPointer<Float>) -> Bool {
        let rms = frameRMS(frame)
        let speech = rms >= max(VAD.minRMS, noiseFloor * VAD.noiseFactor) && zeroCrossingRate(frame) <= VAD.maxZCR
        let window = max(1, Int((VAD.noiseWindowSeconds / VAD.frameSeconds).rounded()))
        if recent.count < window {
            recent.append(rms)
        } else {
            recent[recentHead] = rms
            recentHead = (recentHead + 1) % window
        }
        noiseFloor = max(VAD.noiseFloorEpsilon, recent.min() ?? rms)
        return speech
    }

    /// Speech decision for each complete 30 ms frame in `samples` (with earlier leftovers).
    mutating func feed(_ samples: UnsafeBufferPointer<Float>, sampleRate: Double) -> [Bool] {
        let size = max(1, Int((sampleRate * VAD.frameSeconds).rounded()))
        pending.append(contentsOf: samples)
        var out: [Bool] = []
        var start = 0
        while pending.count - start >= size {
            let frame = pending[start..<(start + size)]
            out.append(frame.withUnsafeBufferPointer { isSpeech($0) })
            start += size
        }
        pending.removeFirst(start)
        return out
    }
}

/// Turn-taking thresholds over the last minute of 30 ms frames.
enum Conversation {
    static let windowSeconds = 60.0
    /// Speech in at least this share of the window…
    static let minSpeechRatio = 0.40
    /// …and at least this many pauses between two stretches of speech…
    static let minTurnPauses = 4
    /// …each this long (shorter gaps are within a sentence, longer ones are silence).
    static let minPauseSeconds = 0.3
    static let maxPauseSeconds = 2.0
    static var windowFrames: Int { Int((windowSeconds / VAD.frameSeconds).rounded()) }
}

/// A rough local proxy for an in-person conversation: enough speech in the last minute, broken
/// by pauses the length of turn changes. No speaker identification.
struct ConversationDetector {
    private var frames = [Bool](repeating: false, count: Conversation.windowFrames)
    private var head = 0 // index of the oldest frame

    mutating func add(_ speech: Bool) {
        frames[head] = speech
        head = (head + 1) % frames.count
    }

    mutating func reset() {
        frames = [Bool](repeating: false, count: Conversation.windowFrames)
        head = 0
    }

    var speechRatio: Double {
        Double(frames.lazy.filter { $0 }.count) / Double(frames.count)
    }

    /// Pauses of 0.3–2 s with speech on both sides, within the window.
    var turnPauses: Int {
        let minRun = Int((Conversation.minPauseSeconds / VAD.frameSeconds).rounded())
        let maxRun = Int((Conversation.maxPauseSeconds / VAD.frameSeconds).rounded())
        var count = 0, run = 0, seenSpeech = false
        for i in 0..<frames.count {
            if frames[(head + i) % frames.count] {
                if seenSpeech && run >= minRun && run <= maxRun { count += 1 }
                seenSpeech = true
                run = 0
            } else {
                run += 1
            }
        }
        return count
    }

    var isConversation: Bool {
        speechRatio >= Conversation.minSpeechRatio && turnPauses >= Conversation.minTurnPauses
    }
}

/// Thread-safe wrapper the mic tap feeds (tap thread) and the recorder polls (main thread).
/// Disabled outside auto mode, so it costs nothing then.
final class ConversationListener {
    private let lock = NSLock()
    private var vad = VoiceActivityDetector()
    private var detector = ConversationDetector()
    private var on = false

    var enabled: Bool {
        get { lock.lock(); defer { lock.unlock() }; return on }
        set {
            lock.lock()
            defer { lock.unlock() }
            if newValue != on { vad = VoiceActivityDetector(); detector.reset() }
            on = newValue
        }
    }

    func feed(_ samples: UnsafeBufferPointer<Float>, sampleRate: Double) {
        lock.lock()
        defer { lock.unlock() }
        guard on else { return }
        for s in vad.feed(samples, sampleRate: sampleRate) { detector.add(s) }
    }

    func reset() {
        lock.lock()
        defer { lock.unlock() }
        vad = VoiceActivityDetector()
        detector.reset()
    }

    var conversation: Bool {
        lock.lock()
        defer { lock.unlock() }
        return on && detector.isConversation
    }
}

// MARK: Auto state machine

enum AutoReason: String {
    case call
    case inPerson = "in_person"
}

/// Auto mode: listening until a call or conversation starts recording; stops `stopAfter`
/// seconds after both signals were last seen. Stepped every 2 s by the recorder.
struct AutoAudio {
    static let stopAfter: TimeInterval = 180

    enum Phase: Equatable { case listening, recording(AutoReason) }
    enum Action: Equatable { case start(AutoReason), stop }

    private(set) var phase = Phase.listening
    private var quietSince: Date?

    var recording: Bool { phase != .listening }

    mutating func step(now: Date, call: Bool, conversation: Bool) -> Action? {
        let reason: AutoReason? = call ? .call : conversation ? .inPerson : nil
        switch phase {
        case .listening:
            guard let r = reason else { return nil }
            phase = .recording(r)
            quietSince = nil
            return .start(r)
        case .recording:
            if let r = reason {
                phase = .recording(r)
                quietSince = nil
                return nil
            }
            let since = quietSince ?? now
            quietSince = since
            guard now.timeIntervalSince(since) >= Self.stopAfter else { return nil }
            reset()
            return .stop
        }
    }

    /// Back to listening (pause, sleep, mode switch).
    mutating func reset() {
        phase = .listening
        quietSince = nil
    }
}

// MARK: Call signal

struct AudioInputProcess {
    let pid: pid_t
    let bundle: String
    let runningInput: Bool
}

/// Processes whose microphone use is ours or not a call: ScreenCaptureKit's daemon runs input
/// for our own system-audio capture.
let ignoredInputBundles: Set<String> = ["com.apple.replayd"]

/// The first process other than us (and not an ignored helper) using audio input.
func firstOtherInput(_ processes: [AudioInputProcess], ownPID: pid_t) -> AudioInputProcess? {
    processes.first { $0.runningInput && $0.pid != ownPID && !ignoredInputBundles.contains($0.bundle) }
}

/// True when a process other than us (and not an ignored helper) is using audio input.
func otherInputActive(_ processes: [AudioInputProcess], ownPID: pid_t) -> Bool {
    firstOtherInput(processes, ownPID: ownPID) != nil
}

/// The app behind a call: its bundle id (logged with `audio_auto_start`) and the name the
/// dashboard shows ("Zoom"). Both are "" when only the device-level fallback saw the call.
struct CallSource: Equatable {
    let bundle: String
    let name: String

    /// Name for the status line: the app name, else the bundle id, else "" (shown as "call").
    var label: String { name.isEmpty ? bundle : name }
}

/// Fallback without the per-process list: the default input device runs somewhere, and not
/// just because our own engine runs it (while it does, other users cannot be told apart).
func deviceFallbackCall(runningSomewhere: Bool, ourEngineRunning: Bool) -> Bool {
    runningSomewhere && !ourEngineRunning
}

/// CoreAudio queries for the call signal (main thread, every 2 s).
enum InputUsage {
    private static func address(_ sel: AudioObjectPropertySelector) -> AudioObjectPropertyAddress {
        AudioObjectPropertyAddress(mSelector: sel, mScope: kAudioObjectPropertyScopeGlobal, mElement: kAudioObjectPropertyElementMain)
    }

    private static func u32(_ obj: AudioObjectID, _ sel: AudioObjectPropertySelector) -> UInt32? {
        var a = address(sel)
        var v: UInt32 = 0
        var size = UInt32(MemoryLayout<UInt32>.size)
        return AudioObjectGetPropertyData(obj, &a, 0, nil, &size, &v) == noErr ? v : nil
    }

    /// Every audio process with its input state; nil when the list is unavailable.
    static func processes() -> [AudioInputProcess]? {
        let system = AudioObjectID(kAudioObjectSystemObject)
        var a = address(kAudioHardwarePropertyProcessObjectList)
        var size: UInt32 = 0
        guard AudioObjectGetPropertyDataSize(system, &a, 0, nil, &size) == noErr else { return nil }
        var ids = [AudioObjectID](repeating: 0, count: Int(size) / MemoryLayout<AudioObjectID>.size)
        guard AudioObjectGetPropertyData(system, &a, 0, nil, &size, &ids) == noErr else { return nil }
        return ids.prefix(Int(size) / MemoryLayout<AudioObjectID>.size).map { id in
            var pa = address(kAudioProcessPropertyPID)
            var pid: pid_t = 0
            var psize = UInt32(MemoryLayout<pid_t>.size)
            _ = AudioObjectGetPropertyData(id, &pa, 0, nil, &psize, &pid)
            var ba = address(kAudioProcessPropertyBundleID)
            var bundle: Unmanaged<CFString>?
            var bsize = UInt32(MemoryLayout<Unmanaged<CFString>?>.size)
            let b = AudioObjectGetPropertyData(id, &ba, 0, nil, &bsize, &bundle) == noErr
                ? (bundle?.takeRetainedValue() as String?) ?? "" : ""
            return AudioInputProcess(pid: pid, bundle: b, runningInput: (u32(id, kAudioProcessPropertyIsRunningInput) ?? 0) != 0)
        }
    }

    /// Whether the default input device is running in any process.
    static func defaultInputRunningSomewhere() -> Bool {
        guard let dev = u32(AudioObjectID(kAudioObjectSystemObject), kAudioHardwarePropertyDefaultInputDevice), dev != 0 else {
            return false
        }
        return (u32(AudioObjectID(dev), kAudioDevicePropertyDeviceIsRunningSomewhere) ?? 0) != 0
    }

    /// The call signal from microphone use: the other process recording, or nil.
    static func otherProcessRecording(ourEngineRunning: Bool) -> CallSource? {
        if let list = processes() {
            guard let p = firstOtherInput(list, ownPID: getpid()) else { return nil }
            return CallSource(bundle: p.bundle, name: NSRunningApplication(processIdentifier: p.pid)?.localizedName ?? "")
        }
        let call = deviceFallbackCall(runningSomewhere: defaultInputRunningSomewhere(), ourEngineRunning: ourEngineRunning)
        return call ? CallSource(bundle: "", name: "") : nil
    }
}

// MARK: Ring buffer

/// One buffered sample buffer: its wall-clock start, original timestamp and mono 16-bit PCM.
struct RingEntry {
    let wall: Date
    let pts: CMTime
    let sampleRate: Double
    let samples: [Int16]
}

/// The last `seconds` of one track, in memory only (mono 16-bit: ~11.5 MB per track at 48 kHz).
struct AudioRing {
    static let seconds: TimeInterval = 120

    private var entries: [RingEntry] = []
    private var head = 0
    private(set) var byteCount = 0

    mutating func append(_ e: RingEntry) {
        entries.append(e)
        byteCount += e.samples.count * MemoryLayout<Int16>.size
        let cutoff = e.wall.addingTimeInterval(-Self.seconds)
        while head < entries.count && entries[head].wall < cutoff {
            byteCount -= entries[head].samples.count * MemoryLayout<Int16>.size
            head += 1
        }
        if head > 256 && head * 2 > entries.count { // compact now and then
            entries.removeFirst(head)
            head = 0
        }
    }

    /// The buffered entries, oldest first; empties the ring.
    mutating func drain() -> [RingEntry] {
        let out = Array(entries[head...])
        clear()
        return out
    }

    mutating func clear() {
        entries = []
        head = 0
        byteCount = 0
    }
}

/// Float samples → clipped 16-bit PCM; all zeros when `muted` (exclusion: silence, timing kept).
func int16Samples(_ x: UnsafeBufferPointer<Float>, muted: Bool) -> [Int16] {
    if muted { return [Int16](repeating: 0, count: x.count) }
    return x.map { Int16((min(1, max(-1, $0)) * 32767).rounded()) }
}

/// Splits ring entries (wall-clock starts, oldest first) at chunk boundaries: each range becomes
/// one chunk named for its first entry's time.
func ringFlushGroups(walls: [Date], chunkMinutes: Int) -> [Range<Int>] {
    var out: [Range<Int>] = []
    var start = 0
    while start < walls.count {
        let end = chunkEnd(after: walls[start], minutes: chunkMinutes)
        var i = start + 1
        while i < walls.count && walls[i] < end { i += 1 }
        out.append(start..<i)
        start = i
    }
    return out
}

/// A free chunk file name for a chunk starting at `start`: `HHMM-<track>.m4a` (unless `exact`),
/// else `HHMMSS-<track>.m4a`, else `HHMMSS-<n>-<track>.m4a` for n = 1…999; nil when all are
/// taken. `exists` is asked about bare file names.
func uniqueChunkName(start: Date, track: String, exact: Bool, exists: (String) -> Bool) -> String? {
    if !exact {
        let short = chunkName(start: start, track: track)
        if !exists(short) { return short }
    }
    let full = exactChunkName(start: start, track: track)
    if !exists(full) { return full }
    let stem = String(full.dropLast("-\(track).m4a".count))
    for n in 1...999 {
        let name = "\(stem)-\(n)-\(track).m4a"
        if !exists(name) { return name }
    }
    return nil
}

/// "HHMMSS-<track>.m4a": flushed chunks start mid-minute, so the name keeps the second.
func exactChunkName(start: Date, track: String) -> String {
    let f = DateFormatter()
    f.locale = Locale(identifier: "en_US_POSIX")
    f.dateFormat = "HHmmss"
    return "\(f.string(from: start))-\(track).m4a"
}

/// Mono Float32 samples of a sample buffer (the first channel); nil for other formats.
func monoFloats(_ sample: CMSampleBuffer) -> (samples: [Float], sampleRate: Double)? {
    guard let fmt = CMSampleBufferGetFormatDescription(sample),
          let asbd = CMAudioFormatDescriptionGetStreamBasicDescription(fmt)?.pointee,
          asbd.mFormatID == kAudioFormatLinearPCM, asbd.mBitsPerChannel == 32,
          asbd.mFormatFlags & kAudioFormatFlagIsFloat != 0,
          let block = CMSampleBufferGetDataBuffer(sample) else { return nil }
    let frames = CMSampleBufferGetNumSamples(sample)
    let channels = max(1, Int(asbd.mChannelsPerFrame))
    let interleaved = asbd.mFormatFlags & kAudioFormatFlagIsNonInterleaved == 0
    let length = CMBlockBufferGetDataLength(block)
    var all = [Float](repeating: 0, count: length / MemoryLayout<Float>.size)
    guard all.withUnsafeMutableBytes({ CMBlockBufferCopyDataBytes(block, atOffset: 0, dataLength: length, destination: $0.baseAddress!) }) == noErr
    else { return nil }
    let out: [Float]
    if channels == 1 {
        out = Array(all.prefix(frames))
    } else if interleaved {
        out = stride(from: 0, to: min(all.count, frames * channels), by: channels).map { all[$0] }
    } else {
        out = Array(all.prefix(frames)) // first plane
    }
    return (out, asbd.mSampleRate)
}

/// A ready mono Float32 LPCM sample buffer of `samples` at `pts`.
func makePCMSample(_ samples: UnsafeBufferPointer<Float>, sampleRate: Double, pts: CMTime) -> CMSampleBuffer? {
    guard !samples.isEmpty,
          let fmt = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: sampleRate, channels: 1, interleaved: false),
          let pcm = AVAudioPCMBuffer(pcmFormat: fmt, frameCapacity: AVAudioFrameCount(samples.count)),
          let dst = pcm.floatChannelData?[0] else { return nil }
    pcm.frameLength = AVAudioFrameCount(samples.count)
    dst.update(from: samples.baseAddress!, count: samples.count)
    var desc: CMAudioFormatDescription?
    guard CMAudioFormatDescriptionCreate(allocator: nil, asbd: fmt.streamDescription, layoutSize: 0, layout: nil,
                                         magicCookieSize: 0, magicCookie: nil, extensions: nil,
                                         formatDescriptionOut: &desc) == noErr else { return nil }
    var timing = CMSampleTimingInfo(duration: CMTime(value: 1, timescale: CMTimeScale(sampleRate)),
                                    presentationTimeStamp: pts, decodeTimeStamp: .invalid)
    var sample: CMSampleBuffer?
    guard CMSampleBufferCreate(allocator: nil, dataBuffer: nil, dataReady: false, makeDataReadyCallback: nil, refcon: nil,
                               formatDescription: desc, sampleCount: CMItemCount(samples.count), sampleTimingEntryCount: 1,
                               sampleTimingArray: &timing, sampleSizeEntryCount: 0, sampleSizeArray: nil,
                               sampleBufferOut: &sample) == noErr, let sb = sample,
          CMSampleBufferSetDataBufferFromAudioBufferList(sb, blockBufferAllocator: nil, blockBufferMemoryAllocator: nil,
                                                         flags: 0, bufferList: pcm.audioBufferList) == noErr
    else { return nil }
    return sb
}

/// Rebuilds a buffered entry as a Float32 sample buffer with its original timestamp.
func sampleBuffer(from e: RingEntry) -> CMSampleBuffer? {
    let floats = e.samples.map { Float($0) / 32767 }
    return floats.withUnsafeBufferPointer { makePCMSample($0, sampleRate: e.sampleRate, pts: e.pts) }
}
