import Foundation
import AVFoundation
import CoreMedia

/// Microphone → `AudioChunkWriter(track: "mic")` via an `AVAudioEngine` input tap, converted
/// to mono Float32 48 kHz. The engine only runs while audio is wanted (written, or buffered in
/// auto mode), so the mic indicator is off during pause, sleep and audio mode off
/// (during exclusion it keeps running and the writer stores silence). Call from the main thread.
/// A mic error is reported only when it differs from the last one reported (cleared when the
/// microphone runs).
func shouldReportMicError(_ message: String, last: String?) -> Bool { message != last }

final class MicRecorder {
    private let writer: AudioChunkWriter
    private let engine = AVAudioEngine()
    private let format = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 48_000, channels: 1, interleaved: false)!
    private var wanted = false
    private var running = false
    private var framesWritten: Int64 = 0
    private var configObserver: NSObjectProtocol?
    var onError: ((String) -> Void)?
    /// Each converted mono 48 kHz buffer, on the tap thread (the auto-mode conversation
    /// listener). Set before the engine starts.
    var onPCM: ((UnsafeBufferPointer<Float>, Double) -> Void)?
    /// Whether our engine is running the input device.
    var isRunning: Bool { running }
    /// Called when the microphone starts recording, clearing any earlier error.
    var onRunning: (() -> Void)?
    private var lastError: String?

    init(writer: AudioChunkWriter) {
        self.writer = writer
        configObserver = NotificationCenter.default.addObserver(
            forName: .AVAudioEngineConfigurationChange, object: engine, queue: .main) { [weak self] _ in
            // Input device changed (e.g. headset plugged in): rebuild the tap for the new format.
            guard let self = self, self.running else { return }
            self.stopEngine()
            self.startEngine()
        }
    }

    deinit {
        if let o = configObserver { NotificationCenter.default.removeObserver(o) }
    }

    func start() {
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

    func stop() {
        wanted = false
        stopEngine()
        writer.finish()
    }

    private func startEngine() {
        guard wanted, !running else { return }
        let input = engine.inputNode
        let inFormat = input.outputFormat(forBus: 0)
        guard inFormat.sampleRate > 0, let converter = AVAudioConverter(from: inFormat, to: format) else {
            fail("no usable microphone input (format \(inFormat))")
            return
        }
        input.installTap(onBus: 0, bufferSize: 4096, format: inFormat) { [weak self] buffer, _ in
            self?.handle(buffer, converter: converter)
        }
        do {
            try engine.start()
            running = true
            lastError = nil
            onRunning?()
        } catch {
            input.removeTap(onBus: 0)
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
        guard running else { return }
        engine.inputNode.removeTap(onBus: 0)
        engine.stop()
        running = false
    }

    /// Runs on the engine's tap thread.
    private func handle(_ buffer: AVAudioPCMBuffer, converter: AVAudioConverter) {
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
