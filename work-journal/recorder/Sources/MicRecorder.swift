import Foundation
import AVFoundation
import CoreMedia
import CoreAudio
import AudioToolbox

/// Microphone → `AudioChunkWriter(track: "mic")` via an `AVAudioEngine` input tap, converted
/// to mono Float32 48 kHz. The engine only runs while audio is wanted (written, or buffered in
/// auto mode), so the mic indicator is off during pause, sleep and audio mode off
/// (during exclusion it keeps running and the writer stores silence). Call from the main thread.
/// A mic error is reported only when it differs from the last one reported (cleared when the
/// microphone runs).
func shouldReportMicError(_ message: String, last: String?) -> Bool { message != last }

/// Decides when a running microphone has gone silent: after the input device changes (a call
/// app taking it, for one) the engine can keep running without delivering any buffers and
/// without an error. Stepped every 2 s by the recorder.
struct MicStallWatch {
    /// No audio for this long while watched counts as a stall; a restart waits this long again.
    static let stallAfter: TimeInterval = 10

    /// `restart`: restart the engine, `notify` on the first restart of a stall. `recovered`:
    /// audio arrived again after a stall. `clear`: the mic is no longer wanted during a stall.
    enum Action: Equatable { case restart(notify: Bool), recovered, clear }

    private(set) var stalled = false
    private var restartedAt: Date?

    /// `watching`: the mic is wanted (and permitted), running or not. `lastAudio`: when the tap
    /// last wrote audio. `startedAt`: when the mic became wanted (restarts do not move it).
    mutating func step(now: Date, watching: Bool, lastAudio: Date, startedAt: Date) -> Action? {
        guard watching else {
            restartedAt = nil
            guard stalled else { return nil }
            stalled = false
            return .clear
        }
        if stalled, let r = restartedAt, lastAudio > r {
            stalled = false
            restartedAt = nil
            return .recovered
        }
        let quietSince = max(lastAudio, startedAt, restartedAt ?? .distantPast)
        guard now.timeIntervalSince(quietSince) >= Self.stallAfter else { return nil }
        let first = !stalled
        stalled = true
        restartedAt = now
        return .restart(notify: first)
    }
}

/// run-tests for the microphone stall watch.
func micStallChecks() -> [(String, Bool)] {
    let t0 = Date(timeIntervalSince1970: 5_000_000)
    func at(_ s: TimeInterval) -> Date { t0.addingTimeInterval(s) }
    let never = Date.distantPast

    var w = MicStallWatch()
    let starting = w.step(now: at(8), watching: true, lastAudio: never, startedAt: t0)
    let flowing = w.step(now: at(30), watching: true, lastAudio: at(25), startedAt: t0)
    let almost = w.step(now: at(34.9), watching: true, lastAudio: at(25), startedAt: t0)
    let stall = w.step(now: at(35), watching: true, lastAudio: at(25), startedAt: t0)
    let waiting = w.step(now: at(37), watching: true, lastAudio: at(25), startedAt: at(35))
    let again = w.step(now: at(45), watching: true, lastAudio: at(25), startedAt: at(35))
    let back = w.step(now: at(47), watching: true, lastAudio: at(46), startedAt: at(45))
    let fine = w.step(now: at(49), watching: true, lastAudio: at(48), startedAt: at(45))

    // A restart that fails to start the engine is retried every 10 s, not every tick.
    var failing = MicStallWatch()
    _ = failing.step(now: at(10), watching: true, lastAudio: never, startedAt: t0)
    let retryWait = failing.step(now: at(12), watching: true, lastAudio: never, startedAt: t0)
    let retry = failing.step(now: at(20), watching: true, lastAudio: never, startedAt: t0)

    var off = MicStallWatch()
    _ = off.step(now: at(10), watching: true, lastAudio: never, startedAt: t0)
    let stopped = off.step(now: at(12), watching: false, lastAudio: never, startedAt: t0)
    let stillOff = off.step(now: at(30), watching: false, lastAudio: never, startedAt: t0)

    var unwatched = MicStallWatch()
    let idle = unwatched.step(now: at(60), watching: false, lastAudio: never, startedAt: t0)

    return [
        ("mic stall: a starting mic gets 10 s", starting == nil),
        ("mic stall: audio flowing is fine", flowing == nil && almost == nil),
        ("mic stall: 10 s of silence restarts and notifies", stall == .restart(notify: true)),
        ("mic stall: a restart gets 10 s again", waiting == nil),
        ("mic stall: still silent restarts without notifying again", again == .restart(notify: false)),
        ("mic stall: audio again recovers once", back == .recovered && fine == nil),
        ("mic stall: a failed restart is retried after 10 s", retryWait == nil && retry == .restart(notify: false)),
        ("mic stall: mic no longer wanted clears", stopped == .clear && stillOff == nil),
        ("mic stall: an unwatched mic never stalls", idle == nil),
    ]
}

/// Which input device the mic records from. The default input, unless it is a Bluetooth
/// headset and the Mac has a built-in microphone: opening a headset's mic switches it to its
/// call profile, and a call app taking it then left our engine running without audio (the
/// whole of the user's side of a call lost). The built-in mic hears the user too, and a
/// device that is not Bluetooth (a USB microphone) is kept as chosen.
func chooseMicDevice(defaultID: UInt32?, defaultIsBluetooth: Bool, builtInID: UInt32?) -> UInt32? {
    guard let d = defaultID, d != 0 else { return builtInID }
    if defaultIsBluetooth, let b = builtInID { return b }
    return d
}

/// run-tests for the microphone device choice.
func micDeviceChecks() -> [(String, Bool)] {
    [
        ("mic device: the default input when it is not Bluetooth",
         chooseMicDevice(defaultID: 7, defaultIsBluetooth: false, builtInID: 3) == 7),
        ("mic device: the built-in mic instead of a Bluetooth headset",
         chooseMicDevice(defaultID: 9, defaultIsBluetooth: true, builtInID: 3) == 3),
        ("mic device: a Bluetooth headset when there is no built-in mic",
         chooseMicDevice(defaultID: 9, defaultIsBluetooth: true, builtInID: nil) == 9),
        ("mic device: the built-in mic when there is no default",
         chooseMicDevice(defaultID: nil, defaultIsBluetooth: false, builtInID: 3) == 3),
    ]
}

/// CoreAudio queries for picking the microphone device.
enum MicDevices {
    private static func address(_ sel: AudioObjectPropertySelector,
                                _ scope: AudioObjectPropertyScope = kAudioObjectPropertyScopeGlobal) -> AudioObjectPropertyAddress {
        AudioObjectPropertyAddress(mSelector: sel, mScope: scope, mElement: kAudioObjectPropertyElementMain)
    }

    private static func u32(_ obj: AudioObjectID, _ sel: AudioObjectPropertySelector) -> UInt32? {
        var a = address(sel)
        var v: UInt32 = 0
        var size = UInt32(MemoryLayout<UInt32>.size)
        return AudioObjectGetPropertyData(obj, &a, 0, nil, &size, &v) == noErr ? v : nil
    }

    static var defaultInput: AudioDeviceID? {
        guard let d = u32(AudioObjectID(kAudioObjectSystemObject), kAudioHardwarePropertyDefaultInputDevice), d != 0 else { return nil }
        return d
    }

    static func isBluetooth(_ dev: AudioDeviceID) -> Bool {
        let t = u32(dev, kAudioDevicePropertyTransportType)
        return t == kAudioDeviceTransportTypeBluetooth || t == kAudioDeviceTransportTypeBluetoothLE
    }

    private static func hasInput(_ dev: AudioDeviceID) -> Bool {
        var a = address(kAudioDevicePropertyStreams, kAudioObjectPropertyScopeInput)
        var size: UInt32 = 0
        return AudioObjectGetPropertyDataSize(dev, &a, 0, nil, &size) == noErr && size > 0
    }

    /// The Mac's built-in microphone, if it has one.
    static var builtInInput: AudioDeviceID? {
        let system = AudioObjectID(kAudioObjectSystemObject)
        var a = address(kAudioHardwarePropertyDevices)
        var size: UInt32 = 0
        guard AudioObjectGetPropertyDataSize(system, &a, 0, nil, &size) == noErr else { return nil }
        var ids = [AudioDeviceID](repeating: 0, count: Int(size) / MemoryLayout<AudioDeviceID>.size)
        guard AudioObjectGetPropertyData(system, &a, 0, nil, &size, &ids) == noErr else { return nil }
        return ids.first { u32($0, kAudioDevicePropertyTransportType) == kAudioDeviceTransportTypeBuiltIn && hasInput($0) }
    }

    static func name(_ dev: AudioDeviceID) -> String {
        var a = address(kAudioObjectPropertyName)
        var name: Unmanaged<CFString>?
        var size = UInt32(MemoryLayout<Unmanaged<CFString>?>.size)
        guard AudioObjectGetPropertyData(dev, &a, 0, nil, &size, &name) == noErr else { return "device \(dev)" }
        return (name?.takeRetainedValue() as String?) ?? "device \(dev)"
    }

    /// The device an input unit records from.
    static func current(_ unit: AudioUnit) -> AudioDeviceID? {
        var id = AudioDeviceID(0)
        var size = UInt32(MemoryLayout<AudioDeviceID>.size)
        guard AudioUnitGetProperty(unit, kAudioOutputUnitProperty_CurrentDevice, kAudioUnitScope_Global, 0, &id, &size) == noErr,
              id != 0 else { return nil }
        return id
    }

    /// The device the mic should record from now (see `chooseMicDevice`).
    static func choose() -> AudioDeviceID? {
        let d = defaultInput
        return chooseMicDevice(defaultID: d, defaultIsBluetooth: d.map(isBluetooth) ?? false, builtInID: builtInInput)
    }
}

final class MicRecorder {
    /// Wait after an input change before restarting, so a burst of changes (a headset
    /// connecting, then switching profile) restarts the engine once.
    static let changeSettle: TimeInterval = 1

    private let writer: AudioChunkWriter
    /// A new engine for every start: one restarted after its input device changed can run
    /// without delivering audio.
    private var engine: AVAudioEngine?
    private let format = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 48_000, channels: 1, interleaved: false)!
    private var wanted = false
    private var running = false
    private var framesWritten: Int64 = 0
    private var configObserver: NSObjectProtocol?
    private var pendingRestart: DispatchWorkItem?
    private var lastDevice: AudioDeviceID?
    /// The input format the running engine started with.
    private var startedFormat: AVAudioFormat?
    var onError: ((String) -> Void)?
    /// Each converted mono 48 kHz buffer, on the tap thread (the auto-mode conversation
    /// listener). Set before the engine starts.
    var onPCM: ((UnsafeBufferPointer<Float>, Double) -> Void)?
    /// Whether our engine is running the input device.
    var isRunning: Bool { running }
    /// Called when the microphone starts recording, clearing any earlier error.
    var onRunning: (() -> Void)?
    private var lastError: String?
    private var stall = MicStallWatch()
    /// When the mic became wanted: the stall watch's grace starts here. Restarts after an
    /// input change do not move it, so a headset that keeps changing cannot hide a dead mic.
    private var wantedSince = Date()
    /// When the tap last wrote audio. Written on the tap thread.
    private let audioLock = NSLock()
    private var lastAudioAt = Date.distantPast
    /// Tap thread only: the converter for the input format last seen.
    private var converter: AVAudioConverter?

    init(writer: AudioChunkWriter) {
        self.writer = writer
    }

    deinit {
        if let o = configObserver { NotificationCenter.default.removeObserver(o) }
    }

    func start() {
        if !wanted { wantedSince = Date() }
        wanted = true
        switch AVCaptureDevice.authorizationStatus(for: .audio) {
        case .authorized:
            startEngine()
        case .notDetermined:
            AVCaptureDevice.requestAccess(for: .audio) { granted in
                DispatchQueue.main.async {
                    if granted { self.startEngine() } else { self.fail("microphone permission denied") }
                }
            }
        default:
            fail("microphone permission denied")
        }
    }

    /// Stops the engine. The recorder finishes the mic chunk through the writer's gate, which
    /// never blocks the main thread behind a flush.
    func stop() {
        wanted = false
        pendingRestart?.cancel()
        pendingRestart = nil
        stopEngine()
    }

    /// Every 2 s: restarts a mic that is wanted but has delivered no audio for
    /// `MicStallWatch.stallAfter` (running silent, or not running after a failed restart).
    func checkStall(now: Date = Date()) -> MicStallWatch.Action? {
        let authorized = AVCaptureDevice.authorizationStatus(for: .audio) == .authorized
        let action = stall.step(now: now, watching: wanted && authorized,
                                lastAudio: lastAudio, startedAt: wantedSince)
        if case .restart? = action {
            pendingRestart?.cancel()
            pendingRestart = nil
            stopEngine()
            startEngine()
        }
        return action
    }

    private var lastAudio: Date {
        audioLock.lock()
        defer { audioLock.unlock() }
        return lastAudioAt
    }

    private func audioWritten() {
        audioLock.lock()
        lastAudioAt = Date()
        audioLock.unlock()
    }

    /// The input device changed (a headset connected, a call app switched its profile): stop
    /// now and start a new engine once the changes settle.
    private func inputChanged() {
        guard wanted else { return }
        // Selecting the device posts a change of its own; an engine still running on the
        // device and format it started with needs nothing (restarting it would post another).
        if running, let engine = engine, engine.isRunning, MicDevices.choose() == lastDevice,
           engine.inputNode.outputFormat(forBus: 0) == startedFormat {
            return
        }
        stopEngine()
        pendingRestart?.cancel()
        let work = DispatchWorkItem { [weak self] in
            guard let self = self else { return }
            self.pendingRestart = nil
            self.startEngine()
        }
        pendingRestart = work
        DispatchQueue.main.asyncAfter(deadline: .now() + Self.changeSettle, execute: work)
    }

    private func startEngine() {
        guard wanted, !running else { return }
        let engine = AVAudioEngine()
        let input = engine.inputNode
        if let dev = MicDevices.choose() {
            // Only when it differs: setting the device posts a configuration change.
            if let unit = input.audioUnit, MicDevices.current(unit) != dev {
                var id = dev
                let status = AudioUnitSetProperty(unit, kAudioOutputUnitProperty_CurrentDevice, kAudioUnitScope_Global, 0,
                                                  &id, UInt32(MemoryLayout<AudioDeviceID>.size))
                if status != noErr { eprint("recorder: cannot select microphone \(MicDevices.name(dev)) (\(status))") }
            }
            if dev != lastDevice {
                eprint("recorder: microphone input \(MicDevices.name(dev))")
                lastDevice = dev
            }
        }
        let inFormat = input.outputFormat(forBus: 0)
        guard inFormat.sampleRate > 0, inFormat.channelCount > 0 else {
            fail("no usable microphone input (format \(inFormat))")
            return
        }
        startedFormat = inFormat
        // nil: the tap takes the bus's own format, which never throws a format mismatch;
        // the converter follows each buffer's format.
        input.installTap(onBus: 0, bufferSize: 4096, format: nil) { [weak self] buffer, _ in
            self?.handle(buffer)
        }
        if let o = configObserver { NotificationCenter.default.removeObserver(o) }
        configObserver = NotificationCenter.default.addObserver(
            forName: .AVAudioEngineConfigurationChange, object: engine, queue: .main) { [weak self] _ in
            self?.inputChanged()
        }
        do {
            try engine.start()
            self.engine = engine
            running = true
            lastError = nil
            onRunning?()
        } catch {
            input.removeTap(onBus: 0)
            if let o = configObserver { NotificationCenter.default.removeObserver(o) }
            configObserver = nil
            fail("cannot start microphone: \(error.localizedDescription)")
        }
    }

    /// Reports an error once: start() runs on every state change, and the same denial
    /// must not be logged again each time.
    private func fail(_ message: String) {
        guard shouldReportMicError(message, last: lastError) else { return }
        lastError = message
        onError?(message)
    }

    private func stopEngine() {
        guard running, let engine = engine else { return }
        if let o = configObserver { NotificationCenter.default.removeObserver(o) }
        configObserver = nil
        engine.inputNode.removeTap(onBus: 0)
        engine.stop()
        self.engine = nil
        running = false
    }

    /// Runs on the engine's tap thread.
    private func handle(_ buffer: AVAudioPCMBuffer) {
        if converter == nil || converter?.inputFormat != buffer.format {
            converter = AVAudioConverter(from: buffer.format, to: format)
        }
        guard let converter = converter else { return }
        let ratio = format.sampleRate / buffer.format.sampleRate
        let capacity = AVAudioFrameCount(Double(buffer.frameLength) * ratio) + 64
        guard let out = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: capacity) else { return }
        var consumed = false
        var error: NSError?
        converter.convert(to: out, error: &error) { _, status in
            if consumed {
                status.pointee = .noDataNow
                return nil
            }
            consumed = true
            status.pointee = .haveData
            return buffer
        }
        guard error == nil, out.frameLength > 0 else { return }
        if let onPCM = onPCM, let data = out.floatChannelData?[0] {
            onPCM(UnsafeBufferPointer(start: data, count: Int(out.frameLength)), format.sampleRate)
        }
        guard let sample = makeSampleBuffer(out) else { return }
        writer.append(sample)
        audioWritten()
    }

    /// Wraps converted PCM in a CMSampleBuffer with a monotonic timestamp.
    private func makeSampleBuffer(_ pcm: AVAudioPCMBuffer) -> CMSampleBuffer? {
        var desc: CMAudioFormatDescription?
        guard CMAudioFormatDescriptionCreate(allocator: nil, asbd: pcm.format.streamDescription, layoutSize: 0,
                                             layout: nil, magicCookieSize: 0, magicCookie: nil, extensions: nil,
                                             formatDescriptionOut: &desc) == noErr else { return nil }
        let rate = CMTimeScale(format.sampleRate)
        var timing = CMSampleTimingInfo(duration: CMTime(value: 1, timescale: rate),
                                        presentationTimeStamp: CMTime(value: framesWritten, timescale: rate),
                                        decodeTimeStamp: .invalid)
        var sample: CMSampleBuffer?
        guard CMSampleBufferCreate(allocator: nil, dataBuffer: nil, dataReady: false, makeDataReadyCallback: nil,
                                   refcon: nil, formatDescription: desc, sampleCount: CMItemCount(pcm.frameLength),
                                   sampleTimingEntryCount: 1, sampleTimingArray: &timing, sampleSizeEntryCount: 0,
                                   sampleSizeArray: nil, sampleBufferOut: &sample) == noErr,
              let sb = sample,
              CMSampleBufferSetDataBufferFromAudioBufferList(sb, blockBufferAllocator: nil, blockBufferMemoryAllocator: nil,
                                                             flags: 0, bufferList: pcm.audioBufferList) == noErr
        else { return nil }
        framesWritten += Int64(pcm.frameLength)
        return sb
    }
}
