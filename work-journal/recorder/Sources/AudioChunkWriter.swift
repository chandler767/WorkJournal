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
/// The gate (`setGate`) drops samples, writes them, or keeps the last `AudioRing.seconds` in
/// memory (auto mode listening) until a start flushes them into chunks.
/// Thread-safe; samples may come from any thread.
final class AudioChunkWriter {
    let track: String
    private let dataDir: URL
    private let chunkMinutes: Int
    private let queue: DispatchQueue
    private var writer: AVAssetWriter?
    private var input: AVAssetWriterInput?
    private var end = Date.distantPast
    private var gate = AudioGate.write
    private var ring = AudioRing()
    private var reportedRingFormat = false
    private var muted = false
    private var reportedFailure = false

    init(track: String, dataDir: URL, chunkMinutes: Int) {
        self.track = track
        self.dataDir = dataDir
        self.chunkMinutes = chunkMinutes
        queue = DispatchQueue(label: "workjournal.audio.\(track)")
    }

    func append(_ sample: CMSampleBuffer) {
        // Wall-clock start of the sample (it has just been captured), for buffered audio.
        let duration = CMTimeGetSeconds(CMSampleBufferGetDuration(sample))
        let wall = Date().addingTimeInterval(duration.isFinite ? -duration : 0)
        queue.async { self.appendOnQueue(sample, wall: wall) }
    }

    /// `.off` finishes the current chunk and clears the buffer (pause, sleep, quit, disk guard,
    /// audio off); `.buffer` finishes the current chunk and starts an empty buffer; `.write`
    /// lets the next sample open a chunk named for that time. With `flush` (an auto start), a
    /// change from `.buffer` to `.write` first writes the buffered audio into chunks named
    /// HHMMSS for its start; without it the buffer is dropped. Blocks until done, except a
    /// flush: it is queued (the writer queue is serial, so later samples follow it) and runs
    /// without holding up the caller while up to two minutes of audio are encoded.
    func setGate(_ g: AudioGate, flush: Bool) {
        let work = { [self] in
            guard g != gate else { return }
            let buffered = gate == .buffer ? ring.drain() : []
            ring.clear()
            finishOnQueue() // only `.write` has a chunk open
            gate = g
            if g == .write && flush { flushOnQueue(buffered) }
        }
        if g == .write && flush { queue.async(execute: work) } else { queue.sync(execute: work) }
    }

    /// Waits until every sample appended so far has been handled.
    func sync() {
        queue.sync {}
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

    private func appendOnQueue(_ original: CMSampleBuffer, wall: Date) {
        guard gate != .off, CMSampleBufferDataIsReady(original), CMSampleBufferGetNumSamples(original) > 0 else { return }
        if gate == .buffer { return bufferOnQueue(original, wall: wall) }
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

    /// Keeps the sample in the ring (silence while muted); nothing is written.
    private func bufferOnQueue(_ sample: CMSampleBuffer, wall: Date) {
        guard let pcm = monoFloats(sample) else {
            if !reportedRingFormat { eprint("audio \(track): cannot buffer this sample format") }
            reportedRingFormat = true
            return
        }
        let samples = pcm.samples.withUnsafeBufferPointer { int16Samples($0, muted: muted) }
        ring.append(RingEntry(wall: wall, pts: CMSampleBufferGetPresentationTimeStamp(sample),
                              sampleRate: pcm.sampleRate, samples: samples))
    }

    /// Writes buffered entries into chunks split at chunk boundaries, each named HHMMSS for its
    /// first entry; the last chunk stays open for the live samples that follow.
    private func flushOnQueue(_ entries: [RingEntry]) {
        for group in ringFlushGroups(walls: entries.map(\.wall), chunkMinutes: chunkMinutes) {
            finishOnQueue()
            let samples = entries[group].compactMap(sampleBuffer(from:))
            guard let first = samples.first, open(at: entries[group.lowerBound].wall, sample: first, exactName: true),
                  let input = input else { return }
            for s in samples {
                // Written faster than real time: wait for the encoder instead of dropping.
                var waited = 0
                while !input.isReadyForMoreMediaData && waited < 2000 {
                    usleep(1000)
                    waited += 1
                }
                guard input.append(s) else {
                    fail("flush append failed: \(writer?.error?.localizedDescription ?? "unknown error")")
                    writer?.cancelWriting()
                    writer = nil
                    self.input = nil
                    return
                }
            }
        }
    }

    private func open(at start: Date, sample: CMSampleBuffer, exactName: Bool = false) -> Bool {
        let dir = rawDayDir(dataDir, start).appendingPathComponent("audio")
        // Same-minute pause/resume: HHMM is taken, so use the exact start second.
        let name = exactName ? exactChunkName(start: start, track: track) : chunkFileName(start: start, track: track) {
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
