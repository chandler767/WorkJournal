import Foundation
import AVFoundation
import CoreMedia

/// End of the chunk containing `date`: the next local wall-clock time whose minutes since
/// midnight are a multiple of `minutes` (never past the next midnight).
func chunkEnd(after date: Date, minutes: Int) -> Date {
    let cal = Calendar.current
    let midnight = cal.startOfDay(for: date)
    let elapsed = Int(date.timeIntervalSince(midnight) / 60)
    let next = (elapsed / minutes + 1) * minutes
    let nextMidnight = cal.date(byAdding: .day, value: 1, to: midnight)!
    return min(midnight.addingTimeInterval(Double(next) * 60), nextMidnight)
}

/// Writes one audio track as AAC mono 32 kbps chunks `raw/<day>/audio/HHMM-<track>.m4a`,
/// rotated on `chunkMinutes` wall-clock boundaries. Thread-safe; samples may come from any thread.
final class AudioChunkWriter {
    let track: String
    private let dataDir: URL
    private let chunkMinutes: Int
    private let queue: DispatchQueue
    private var writer: AVAssetWriter?
    private var input: AVAssetWriterInput?
    private var end = Date.distantPast
    private var enabled = true
    private var blockedUntil = Date.distantPast
    private var reportedFailure = false

    init(track: String, dataDir: URL, chunkMinutes: Int) {
        self.track = track
        self.dataDir = dataDir
        self.chunkMinutes = chunkMinutes
        queue = DispatchQueue(label: "workjournal.audio.\(track)")
    }

    func append(_ sample: CMSampleBuffer) {
        queue.async { self.appendOnQueue(sample) }
    }

    /// Disabling finishes the current chunk (pause, exclusion, sleep, disk guard); enabling
    /// lets the next sample open a chunk named for that time.
    func setEnabled(_ on: Bool) {
        queue.sync {
            enabled = on
            if !on { finishOnQueue() }
        }
    }

    /// Finishes the current chunk so the `.m4a` is playable. Blocks until written.
    func finish() {
        queue.sync { finishOnQueue() }
    }

    private func appendOnQueue(_ sample: CMSampleBuffer) {
        guard enabled, CMSampleBufferDataIsReady(sample), CMSampleBufferGetNumSamples(sample) > 0 else { return }
        let now = Date()
        if writer != nil && now >= end { finishOnQueue() }
        if writer == nil {
            guard now >= blockedUntil, open(at: now, sample: sample) else { return }
        }
        guard let writer = writer, let input = input else { return }
        guard input.isReadyForMoreMediaData else { return }
        if !input.append(sample) {
            fail("append failed: \(writer.error?.localizedDescription ?? "unknown error")")
            writer.cancelWriting()
            self.writer = nil
            self.input = nil
        }
    }

    private func open(at start: Date, sample: CMSampleBuffer) -> Bool {
        let dir = rawDayDir(dataDir, start).appendingPathComponent("audio")
        let url = dir.appendingPathComponent(chunkName(start: start, track: track))
        if FileManager.default.fileExists(atPath: url.path) {
            // Same-minute pause/resume: the contract name is taken, so wait for the next minute
            // rather than overwrite the earlier chunk.
            let minute = floor(start.timeIntervalSinceReferenceDate / 60) * 60 + 60
            blockedUntil = Date(timeIntervalSinceReferenceDate: minute)
            return false
        }
        do {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            let w = try AVAssetWriter(outputURL: url, fileType: .m4a)
            let settings: [String: Any] = [
                AVFormatIDKey: kAudioFormatMPEG4AAC,
                AVNumberOfChannelsKey: 1,
                AVSampleRateKey: 48_000,
                AVEncoderBitRateKey: 32_000,
            ]
            let inp = AVAssetWriterInput(mediaType: .audio, outputSettings: settings,
                                         sourceFormatHint: CMSampleBufferGetFormatDescription(sample))
            inp.expectsMediaDataInRealTime = true
            guard w.canAdd(inp) else { throw RunOptionsError("cannot add AAC input") }
            w.add(inp)
            guard w.startWriting() else { throw w.error ?? RunOptionsError("startWriting failed") }
            w.startSession(atSourceTime: CMSampleBufferGetPresentationTimeStamp(sample))
            writer = w
            input = inp
            end = chunkEnd(after: start, minutes: chunkMinutes)
            reportedFailure = false
            return true
        } catch {
            fail("cannot open \(url.lastPathComponent): \(error.localizedDescription)")
            try? FileManager.default.removeItem(at: url)
            return false
        }
    }

    private func finishOnQueue() {
        guard let w = writer, let inp = input else { return }
        writer = nil
        input = nil
        guard w.status == .writing else {
            w.cancelWriting()
            return
        }
        inp.markAsFinished()
        let done = DispatchSemaphore(value: 0)
        w.finishWriting { done.signal() }
        done.wait()
        if w.status != .completed {
            fail("finishing \(w.outputURL.lastPathComponent): \(w.error?.localizedDescription ?? "unknown error")")
        }
    }

    private func fail(_ message: String) {
        guard !reportedFailure else { return }
        reportedFailure = true
        eprint("audio \(track): \(message)")
    }
}
