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

/// Whether the open chunk ending at `end` must be finished at `now`. Both the sample path and
/// the boundary timer use it, so a chunk is finalized at its boundary even in silence.
func chunkDue(now: Date, end: Date) -> Bool { now >= end }

/// Wall-clock delay until a chunk ending at `end` is due (0 when already due).
func chunkFinishDelay(now: Date, end: Date) -> TimeInterval { max(0, end.timeIntervalSince(now)) }

/// Writes one audio track as AAC mono 32 kbps chunks `raw/<day>/audio/HHMM-<track>.m4a`
/// (`HHMMSS-…` when that name is taken), rotated on `chunkMinutes` wall-clock boundaries.
/// While muted (exclusion) samples are written as silence with their original timing.
/// Thread-safe; samples may come from any thread.
final class AudioChunkWriter {
    let track: String
    private let dataDir: URL
    private let chunkMinutes: Int
    private let queue: DispatchQueue
    private var writer: AVAssetWriter?
    private var input: AVAssetWriterInput?
    private var end = Date.distantPast
    private var enabled = true
    private var muted = false
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

    /// Disabling finishes the current chunk (pause, sleep, quit, disk guard); enabling
    /// lets the next sample open a chunk named for that time.
    func setEnabled(_ on: Bool) {
        queue.sync {
            enabled = on
            if !on { finishOnQueue() }
        }
    }

    /// Muted samples are replaced by silence before writing; the chunk stays open.
    func setMuted(_ on: Bool) {
        queue.sync { muted = on }
    }

    /// Finishes the current chunk (playable `.m4a`, blocks until written) and leaves the writer
    /// enabled, so the next sample opens a new chunk named for that time (`HHMMSS` when `HHMM`
    /// is taken). A no-op when no chunk is open (disabled, or no sample yet).
    func rotate() {
        queue.sync { finishOnQueue() }
    }

    /// Finishes the current chunk so the `.m4a` is playable. Blocks until written.
    func finish() {
        queue.sync { finishOnQueue() }
    }

    private func appendOnQueue(_ original: CMSampleBuffer) {
        guard enabled, CMSampleBufferDataIsReady(original), CMSampleBufferGetNumSamples(original) > 0 else { return }
        guard let sample = muted ? silenced(original) : original else {
            return fail("cannot build silent sample")
        }
        let now = Date()
        if writer != nil && chunkDue(now: now, end: end) { finishOnQueue() }
        if writer == nil {
            guard open(at: now, sample: sample) else { return }
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
        // Same-minute pause/resume: HHMM is taken, so use the exact start second.
        let name = chunkFileName(start: start, track: track) {
            FileManager.default.fileExists(atPath: dir.appendingPathComponent($0).path)
        }
        let url = dir.appendingPathComponent(name)
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
            scheduleBoundaryFinish(for: w)
            return true
        } catch {
            fail("cannot open \(url.lastPathComponent): \(error.localizedDescription)")
            try? FileManager.default.removeItem(at: url)
            return false
        }
    }

    /// Finishes the chunk at its end time even when no sample arrives (silence: the system
    /// tap delivers nothing), so the file is playable at its boundary. Wall-clock based, so
    /// it also fires right after a wake.
    private func scheduleBoundaryFinish(for w: AVAssetWriter) {
        let delay = chunkFinishDelay(now: Date(), end: end)
        queue.asyncAfter(wallDeadline: .now() + delay) { [weak self] in
            guard let self = self, self.writer === w else { return }
            if chunkDue(now: Date(), end: self.end) {
                self.finishOnQueue()
            } else {
                self.scheduleBoundaryFinish(for: w) // fired early
            }
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

/// A copy of an LPCM sample buffer with the same format, sample count and timing, all zeros.
private func silenced(_ sample: CMSampleBuffer) -> CMSampleBuffer? {
    guard let source = CMSampleBufferGetDataBuffer(sample),
          let format = CMSampleBufferGetFormatDescription(sample) else { return nil }
    let length = CMBlockBufferGetDataLength(source)
    var block: CMBlockBuffer?
    guard CMBlockBufferCreateWithMemoryBlock(allocator: nil, memoryBlock: nil, blockLength: length, blockAllocator: nil,
                                             customBlockSource: nil, offsetToData: 0, dataLength: length,
                                             flags: kCMBlockBufferAssureMemoryNowFlag, blockBufferOut: &block) == noErr,
          let zeros = block,
          CMBlockBufferFillDataBytes(with: 0, blockBuffer: zeros, offsetIntoDestination: 0, dataLength: length) == noErr
    else { return nil }
    var out: CMSampleBuffer?
    guard CMAudioSampleBufferCreateReadyWithPacketDescriptions(
        allocator: nil, dataBuffer: zeros, formatDescription: format, sampleCount: CMSampleBufferGetNumSamples(sample),
        presentationTimeStamp: CMSampleBufferGetPresentationTimeStamp(sample), packetDescriptions: nil,
        sampleBufferOut: &out) == noErr else { return nil }
    return out
}
