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
    /// Longest stretch a flush keeps the writer queue busy before yielding, so a synchronous
    /// call (rotate, from the main thread) waits at most about this long behind it.
    static let flushSlice: TimeInterval = 0.15
    /// Longest `rotate` spends writing out a flush in progress (per track; the recorder rotates
    /// both, so ≤ 30 s in all, under the 35 s Go allows for `rotate`).
    static let rotateFlushLimit: TimeInterval = 15
    /// Longest `finish`/`close` spend writing out a flush in progress.
    static let closeFlushLimit: TimeInterval = 30

    /// A sample waiting to be written in order after a start: buffered audio, then the live
    /// samples that arrived while it was being written.
    private enum Pending {
        case ring(RingEntry)
        case live(CMSampleBuffer, wall: Date)
        case ready(CMSampleBuffer, wall: Date, exact: Bool) // converted, waiting for the encoder
        case written // released once written
    }

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
    /// Queue only: samples still to write after an auto start (oldest first) and the flush run.
    private var pending: [Pending] = []
    private var pendingIndex = 0
    private var flushGeneration = 0
    private var flushing: Bool { pendingIndex < pending.count }
    /// Caller side (one thread, the main thread in the app): the last gate and mute requested,
    /// so repeated `apply` calls cost nothing.
    private var requestedGate = AudioGate.write
    private var requestedMuted = false

    /// This writer's flush slice (`flushSlice`; run-tests shorten it to force a flush across
    /// many slices).
    private let slice: TimeInterval

    init(track: String, dataDir: URL, chunkMinutes: Int, flushSlice: TimeInterval = AudioChunkWriter.flushSlice) {
        self.track = track
        slice = flushSlice
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

    /// `.off` finishes the current chunk and clears the buffer (pause, sleep, disk guard, audio
    /// off); `.buffer` finishes the current chunk and starts an empty buffer; `.write` lets the
    /// next sample open a chunk named for that time. With `flush` (an auto start), a change from
    /// `.buffer` to `.write` first writes the buffered audio into chunks named HHMMSS for its
    /// start, in slices of `flushSlice`, with live samples queued behind it; without it the
    /// buffer is dropped. Leaving `.write` drops what a flush has not written yet.
    /// Never blocks: the change is queued, and a repeat of the last request does nothing.
    func setGate(_ g: AudioGate, flush: Bool) {
        guard g != requestedGate else { return }
        requestedGate = g
        queue.async { self.gateOnQueue(g, flush: flush) }
    }

    /// Muted samples are replaced by silence (buffered ones too); the chunk stays open.
    /// Never blocks; a repeat does nothing.
    func setMuted(_ on: Bool) {
        guard on != requestedMuted else { return }
        requestedMuted = on
        queue.async { self.muted = on }
    }

    /// Waits until every sample and change requested so far has been handled (a flush in
    /// progress may still have slices queued).
    func sync() {
        queue.sync {}
    }

    /// Finishes the current chunk (playable `.m4a`, blocks until written) and leaves the writer
    /// enabled, so the next sample opens a new chunk named for that time (`HHMMSS` when `HHMM`
    /// is taken). A no-op when no chunk is open (disabled, buffering, or no sample yet). A flush
    /// in progress is written out first (at most `rotateFlushLimit`, else abandoned and
    /// logged), so every chunk started before the rotate is complete when it returns and live
    /// audio continues in a chunk opened after it.
    func rotate() {
        queue.sync {
            flushStep(generation: flushGeneration, run: .complete(limit: Self.rotateFlushLimit))
            finishOnQueue()
        }
    }

    /// Writes whatever a flush still holds, then finishes the current chunk so the `.m4a` is
    /// playable. Blocks until written.
    func finish() {
        queue.sync {
            flushStep(generation: flushGeneration, run: .complete(limit: Self.closeFlushLimit))
            finishOnQueue()
        }
    }

    /// Finishes the open chunk without waiting (the capture stream stopped); during a flush the
    /// flush keeps the chunk and finishes it in order.
    func endChunkSoon() {
        queue.async {
            if !self.flushing { self.finishOnQueue() }
        }
    }

    /// Quit: like `finish`, then stops taking samples. Blocks until written.
    func close() {
        requestedGate = .off
        queue.sync {
            flushStep(generation: flushGeneration, run: .complete(limit: Self.closeFlushLimit))
            gateOnQueue(.off, flush: false)
        }
    }

    private func gateOnQueue(_ g: AudioGate, flush: Bool) {
        guard g != gate else { return }
        let buffered = gate == .buffer ? ring.drain() : []
        ring.clear()
        pending = []
        pendingIndex = 0
        flushGeneration += 1
        finishOnQueue() // only `.write` has a chunk open
        gate = g
        if g == .write && flush && !buffered.isEmpty {
            pending = buffered.map { .ring($0) }
            flushStep(generation: flushGeneration, run: .slice)
        }
    }

    private func appendOnQueue(_ original: CMSampleBuffer, wall: Date) {
        guard gate != .off, CMSampleBufferDataIsReady(original), CMSampleBufferGetNumSamples(original) > 0 else { return }
        if gate == .buffer { return bufferOnQueue(original, wall: wall) }
        guard let sample = muted ? silenced(original) : original else {
            return fail("cannot build silent sample")
        }
        if flushing { return pending.append(.live(sample, wall: wall)) } // after the buffered audio
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

    /// How a flush step ends: after one slice (then the next slice is queued), or only when
    /// everything pending is written, giving up (logged) after `limit` seconds.
    private enum FlushRun {
        case slice
        case complete(limit: TimeInterval)
    }

    /// Writes pending samples in order. Each one goes into the open chunk unless it starts at
    /// or after that chunk's end (the same split as `ringFlushGroups`): buffered audio then
    /// opens a chunk named HHMMSS for its own time, a live sample one named for its time. An
    /// entry is converted once, just before its first write attempt, and released once
    /// written. A stale step (the gate changed since) does nothing.
    private func flushStep(generation: Int, run: FlushRun) {
        guard generation == flushGeneration else { return }
        let deadline: Date
        switch run {
        case .slice: deadline = Date().addingTimeInterval(slice)
        case .complete(let limit): deadline = Date().addingTimeInterval(limit)
        }
        while flushing {
            let s: CMSampleBuffer, wall: Date, exact: Bool
            switch pending[pendingIndex] {
            case .written:
                pendingIndex += 1
                continue
            case .ring(let e):
                guard let converted = sampleBuffer(from: e) else { pendingIndex += 1; continue }
                pending[pendingIndex] = .ready(converted, wall: e.wall, exact: true) // drops the 16-bit copy
                continue
            case .live(let sample, let w): (s, wall, exact) = (sample, w, false)
            case .ready(let sample, let w, let x): (s, wall, exact) = (sample, w, x)
            }
            if writer != nil && wall >= end { finishOnQueue() }
            if writer == nil && !open(at: wall, sample: s, exactName: exact) { return abandonFlush() }
            guard let input = input else { return abandonFlush() }
            // Written faster than real time: wait for the encoder, never past the deadline.
            while !input.isReadyForMoreMediaData && Date() < deadline { usleep(1000) }
            if !input.isReadyForMoreMediaData { break }
            if !input.append(s) {
                fail("flush append failed: \(writer?.error?.localizedDescription ?? "unknown error")")
                writer?.cancelWriting()
                writer = nil
                self.input = nil
                return abandonFlush()
            }
            pending[pendingIndex] = .written
            pendingIndex += 1
            if Date() >= deadline { break }
        }
        guard flushing else {
            pending = []
            pendingIndex = 0
            return
        }
        switch run {
        case .slice: queue.async { self.flushStep(generation: generation, run: run) }
        case .complete(let limit): abandonFlush(reason: "not written within \(Int(limit)) s")
        }
    }

    private func abandonFlush(reason: String = "write failed") {
        if flushing { eprint("audio \(track): flush abandoned (\(reason)) with \(pending.count - pendingIndex) samples unwritten") }
        pending = []
        pendingIndex = 0
        flushGeneration += 1
    }

    private func open(at start: Date, sample: CMSampleBuffer, exactName: Bool = false) -> Bool {
        let dir = rawDayDir(dataDir, start).appendingPathComponent("audio")
        // Same-minute pause/resume: HHMM is taken, so use the exact start second (then a suffix).
        guard let name = uniqueChunkName(start: start, track: track, exact: exactName, exists: {
            FileManager.default.fileExists(atPath: dir.appendingPathComponent($0).path)
        }) else {
            fail("no free chunk name for \(exactChunkName(start: start, track: track))")
            return false
        }
        let url = dir.appendingPathComponent(name)
        // Never touch a file this call did not create (another chunk, or one that appeared).
        var mayRemove = false
        do {
            guard !FileManager.default.fileExists(atPath: url.path) else {
                throw RunOptionsError("\(name) already exists")
            }
            mayRemove = true
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
            if mayRemove { try? FileManager.default.removeItem(at: url) }
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
            if self.flushing {
                self.scheduleBoundaryFinishLater(for: w) // the flush splits at boundaries itself
            } else if chunkDue(now: Date(), end: self.end) {
                self.finishOnQueue()
            } else {
                self.scheduleBoundaryFinish(for: w) // fired early
            }
        }
    }

    private func scheduleBoundaryFinishLater(for w: AVAssetWriter) {
        queue.asyncAfter(deadline: .now() + slice) { [weak self] in
            guard let self = self, self.writer === w else { return }
            self.scheduleBoundaryFinish(for: w)
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
