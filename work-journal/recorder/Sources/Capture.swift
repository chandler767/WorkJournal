import Foundation
import AppKit
import ScreenCaptureKit
import CoreMedia
import ImageIO
import UniformTypeIdentifiers

/// One `SCStream` on the main display carrying system audio to the "system" writer and a
/// low-resolution change watcher (~2 fps, 320 px wide), plus change-driven `SCScreenshotManager`
/// screenshots (main display, or all displays side by side in arrangement order, each scaled
/// to `--width`) written as `raw/<day>/screens/HHMMSS.jpg`. A screenshot is taken on an app or
/// window switch, when the watcher sees the screen change past the sensitivity threshold, or
/// after the idle backstop, never closer than the min gap (see `nextShot`). Control from the main thread.
/// When displays change (connected, removed, rearranged) the stream and display list go
/// stale. A screen-parameters change or the stream's stop error restarts the stream; a round
/// of failed screenshots only re-queries the displays used for screenshots, so system audio
/// keeps flowing. Both are debounced and backed off.
final class Capture: NSObject, SCStreamOutput, SCStreamDelegate {
    /// Gate for screenshots (pause, exclusion, sleep, disk guard).
    var screensEnabled = false {
        didSet {
            if !screensEnabled { generation += 1 } else if !oldValue { trigger(.resumed) }
        }
    }
    /// Shortens the idle backstop to the meeting interval.
    var meeting = false {
        didSet { if meeting != oldValue { reschedule() } }
    }
    var onError: ((String) -> Void)?
    var onRunning: (() -> Void)?
    /// Synchronous exclusion re-check of the current frontmost app/title; true drops the frame.
    var isExcludedNow: () -> Bool = { false }

    private let options: RunOptions
    private let systemWriter: AudioChunkWriter
    private let audioQueue = DispatchQueue(label: "workjournal.capture.audio")
    private let encodeQueue = DispatchQueue(label: "workjournal.capture.jpeg")
    private let watchQueue = DispatchQueue(label: "workjournal.capture.watch")
    private var watchRef: [UInt8]? // watchQueue only: watcher thumbnail at the last change
    private var stream: SCStream?
    private var displays: [SCDisplay] = []
    private var active = false
    private var shotTimer: Timer?
    private var retryTimer: Timer?
    private var shooting = false
    private var generation = 0
    private var restartGeneration = 0 // bumped by restart/stop; stale display queries are ignored
    private var lastShot: Date?
    private var pending = Set<ShotTrigger>()
    private var displaysStale = false // every display failed; no shots until displays are refreshed
    private var lastThumb: [UInt8]?
    private var debounce = RestartDebounce(delay: 2)
    private var restartTimer: Timer?
    private var lastFast: [RecoveryAction: Date] = [:] // backoff per recovery kind
    private var pendingAction: RecoveryAction?
    private var screenObserver: NSObjectProtocol?

    init(options: RunOptions, systemWriter: AudioChunkWriter) {
        self.options = options
        self.systemWriter = systemWriter
        super.init()
        screenObserver = NotificationCenter.default.addObserver(
            forName: NSApplication.didChangeScreenParametersNotification, object: nil, queue: .main) { [weak self] _ in
            guard let self = self, self.active else { return }
            eprint("capture: screen parameters changed; restarting capture")
            self.requestRecovery(.screenParametersChanged, at: Date())
        }
    }

    deinit {
        if let o = screenObserver { NotificationCenter.default.removeObserver(o) }
    }

    func start() {
        active = true
        SCShareableContent.getExcludingDesktopWindows(false, onScreenWindowsOnly: true) { content, error in
            DispatchQueue.main.async { self.startStream(content: content, error: error) }
        }
    }

    /// Stops the stream and screenshots; `done` runs on the main thread once the stream stopped.
    func stop(_ done: @escaping () -> Void = {}) {
        active = false
        shotTimer?.invalidate()
        retryTimer?.invalidate()
        restartTimer?.invalidate()
        shotTimer = nil
        retryTimer = nil
        restartTimer = nil
        pendingAction = nil
        debounce = RestartDebounce(delay: 2)
        generation += 1
        restartGeneration += 1
        pending.removeAll()
        displaysStale = false
        guard let s = stream else { return done() }
        stream = nil
        s.stopCapture { _ in DispatchQueue.main.async(execute: done) }
    }

    private func startStream(content: SCShareableContent?, error: Error?) {
        guard active, stream == nil else { return } // a second in-flight query lost the race
        guard let content = content, let picked = pickDisplays(content) else {
            return retry("screen capture unavailable: \(error?.localizedDescription ?? "no display") (Screen Recording permission?)")
        }
        let main = picked.main
        displays = picked.displays
        let config = SCStreamConfiguration()
        config.capturesAudio = true
        config.sampleRate = 48_000
        config.channelCount = 1
        config.excludesCurrentProcessAudio = true
        // Change watcher only (screenshots use SCScreenshotManager): small, 2 fps, no cursor.
        config.width = 320
        config.height = max(1, 320 * main.height / max(1, main.width))
        config.minimumFrameInterval = CMTime(value: 1, timescale: 2)
        config.pixelFormat = kCVPixelFormatType_32BGRA
        config.showsCursor = false
        config.queueDepth = 3
        let s = SCStream(filter: SCContentFilter(display: main, excludingWindows: []), configuration: config, delegate: self)
        do {
            try s.addStreamOutput(self, type: .audio, sampleHandlerQueue: audioQueue)
            try s.addStreamOutput(self, type: .screen, sampleHandlerQueue: watchQueue)
        } catch {
            return retry("cannot add stream output: \(error.localizedDescription)")
        }
        stream = s
        s.startCapture { error in
            DispatchQueue.main.async {
                if let error = error {
                    self.stream = nil
                    self.retry("cannot start capture: \(error.localizedDescription)")
                } else {
                    self.onRunning?()
                    self.shootSoon()
                }
            }
        }
    }

    /// The main display (audio stream filter) and the displays to screenshot.
    private func pickDisplays(_ content: SCShareableContent) -> (main: SCDisplay, displays: [SCDisplay])? {
        guard let main = content.displays.first(where: { $0.displayID == CGMainDisplayID() }) ?? content.displays.first else {
            return nil
        }
        let all = options.allDisplays ? arrangementOrder(content.displays.map(\.frame)).map { content.displays[$0] } : [main]
        return (main, all)
    }

    // MARK: Restart

    /// Schedules a debounced recovery at the request time plus `delay` (later requests in a
    /// burst push it back); a stream restart supersedes a pending display refresh.
    private func requestRecovery(_ failure: CaptureFailure, at now: Date, delay: TimeInterval = 2) {
        guard active else { return }
        pendingAction = mergeRecovery(pendingAction, recoveryAction(for: failure))
        debounce = RestartDebounce(delay: delay)
        debounce.request(at: now)
        armRestartTimer()
    }

    private func armRestartTimer() {
        restartTimer?.invalidate()
        guard let wait = debounce.wait(from: Date()) else { return }
        restartTimer = Timer.scheduledTimer(withTimeInterval: wait, repeats: false) { [weak self] _ in
            guard let self = self else { return }
            guard self.debounce.fire(at: Date()) else { return self.armRestartTimer() }
            let action = self.pendingAction
            self.pendingAction = nil
            switch action {
            case .restartStream?: self.restart()
            case .refreshDisplays?: self.refreshDisplays()
            case nil: break
            }
        }
    }

    /// Stops the current stream (if any) and starts again from a fresh shareable-content
    /// query. The system writer keeps its chunk open; its own boundary logic still applies.
    private func restart() {
        guard active else { return }
        retryTimer?.invalidate()
        retryTimer = nil
        shotTimer?.invalidate()
        shotTimer = nil
        generation += 1
        restartGeneration += 1
        lastThumb = nil
        guard let old = stream else { return start() }
        stream = nil
        var started = false
        let again = { [weak self] in
            guard let self = self, !started, self.active, self.stream == nil else { return }
            started = true
            self.start()
        }
        old.stopCapture { _ in DispatchQueue.main.async(execute: again) }
        DispatchQueue.main.asyncAfter(deadline: .now() + 3, execute: again) // stopCapture may never call back
    }

    /// Re-queries the shareable content and replaces the screenshot display list, leaving the
    /// audio stream running. With no stream (a restart or retry owns recovery) it does nothing.
    private func refreshDisplays() {
        guard active, stream != nil else { return }
        let rg = restartGeneration
        SCShareableContent.getExcludingDesktopWindows(false, onScreenWindowsOnly: true) { content, error in
            DispatchQueue.main.async {
                // A stream restart meanwhile owns the display list; this answer is stale.
                guard self.active, self.stream != nil, rg == self.restartGeneration else { return }
                guard let content = content, let picked = self.pickDisplays(content) else {
                    return self.recoverAfterFailure(.screenshotsFailed,
                                                    "screen capture unavailable: \(error?.localizedDescription ?? "no display")")
                }
                self.displays = picked.displays
                self.lastThumb = nil
                self.generation += 1
                self.shootSoon()
            }
        }
    }

    /// Recovery after a failure: fast the first time, then backed off. A pending recovery is not
    /// pushed back, but a stream restart still supersedes a pending refresh.
    private func recoverAfterFailure(_ failure: CaptureFailure, _ message: String) {
        onError?(message)
        guard debounce.deadline == nil else {
            pendingAction = mergeRecovery(pendingAction, recoveryAction(for: failure))
            return
        }
        let now = Date()
        let kind = recoveryAction(for: failure)
        let delay = stopErrorRestartDelay(now: now, lastFast: lastFast[kind])
        if delay < 30 { lastFast[kind] = now }
        requestRecovery(failure, at: now, delay: delay)
    }

    private func retry(_ message: String) {
        onError?(message)
        guard active else { return }
        retryTimer?.invalidate()
        retryTimer = Timer.scheduledTimer(withTimeInterval: 30, repeats: false) { [weak self] _ in
            guard let self = self, self.active, self.stream == nil else { return }
            self.start()
        }
    }

    // MARK: SCStreamOutput / SCStreamDelegate

    func stream(_ stream: SCStream, didOutputSampleBuffer sampleBuffer: CMSampleBuffer, of type: SCStreamOutputType) {
        switch type {
        case .audio: systemWriter.append(sampleBuffer)
        case .screen: watch(sampleBuffer)
        default: break
        }
    }

    /// watchQueue: compares each complete watcher frame with the reference frame and asks for
    /// a screenshot when it moved past the sensitivity threshold. `.idle` frames (nothing
    /// changed) are skipped without touching pixels.
    private func watch(_ sampleBuffer: CMSampleBuffer) {
        guard let infos = CMSampleBufferGetSampleAttachmentsArray(sampleBuffer, createIfNecessary: false)
                as? [[SCStreamFrameInfo: Any]],
              let raw = infos.first?[.status] as? Int, SCFrameStatus(rawValue: raw) == .complete,
              let pb = CMSampleBufferGetImageBuffer(sampleBuffer) else { return }
        CVPixelBufferLockBaseAddress(pb, .readOnly)
        defer { CVPixelBufferUnlockBaseAddress(pb, .readOnly) }
        guard CVPixelBufferGetPixelFormatType(pb) == kCVPixelFormatType_32BGRA,
              let base = CVPixelBufferGetBaseAddress(pb) else { return }
        let thumb = grayThumb(bgra: base, width: CVPixelBufferGetWidth(pb), height: CVPixelBufferGetHeight(pb),
                              bytesPerRow: CVPixelBufferGetBytesPerRow(pb))
        guard watchRef != nil else { watchRef = thumb; return }
        guard contentChanged(ref: watchRef, next: thumb, threshold: options.shots.sensitivity.threshold) else { return }
        watchRef = thumb
        DispatchQueue.main.async { self.trigger(.contentChange) }
    }

    func stream(_ stream: SCStream, didStopWithError error: Error) {
        DispatchQueue.main.async {
            guard self.stream === stream else { return }
            self.stream = nil
            self.shotTimer?.invalidate()
            self.shotTimer = nil
            self.systemWriter.rotate() // finish the open chunk; never waits out a whole flush
            self.recoverAfterFailure(.streamStopped, "capture stopped: \(error.localizedDescription)")
        }
    }

    // MARK: Screenshots

    /// Asks for a screenshot (an app switch, a screen change, screens re-enabled); it is taken
    /// now or at the end of the min gap, coalesced with other pending triggers.
    func trigger(_ t: ShotTrigger) {
        guard active, stream != nil else { return }
        pending.insert(t)
        reschedule()
    }

    /// Next screenshot as soon as possible, ignoring the gap (stream (re)started, displays refreshed).
    private func shootSoon() {
        displaysStale = false
        lastShot = nil
        reschedule()
    }

    /// Re-evaluates `nextShot` and either shoots or arms the timer for the next evaluation. While
    /// a screenshot is in flight it waits; the shot's completion re-evaluates.
    private func reschedule() {
        shotTimer?.invalidate()
        shotTimer = nil
        guard active, stream != nil, !shooting, !displaysStale else { return }
        switch nextShot(now: Date(), lastShot: lastShot, pending: pending, meeting: meeting, options.shots) {
        case .shootNow:
            pending.removeAll()
            lastShot = Date() // also when gated (paused, excluded): the next try is a backstop away
            if !shoot() { reschedule() }
        case .at(let due):
            let t = Timer(fire: due, interval: 0, repeats: false) { [weak self] _ in self?.reschedule() }
            RunLoop.main.add(t, forMode: .common)
            shotTimer = t
        }
    }

    /// Starts a screenshot; false when gated (nothing in flight, so the caller reschedules).
    private func shoot() -> Bool {
        guard screensEnabled, !shooting, !displays.isEmpty else { return false }
        // Title-only exclusions (a new Private/Incognito window) fire no activation notification.
        guard !isExcludedNow(), screensEnabled else { return false }
        shooting = true
        let taken = Date()
        let gen = generation
        let sizes = screenshotSizes(displays: displays.map { ($0.width, $0.height) }, width: options.width)
        var images = [CGImage?](repeating: nil, count: displays.count)
        var failures = 0
        let group = DispatchGroup()
        for (i, d) in displays.enumerated() {
            let config = SCStreamConfiguration()
            config.width = sizes[i].width
            config.height = sizes[i].height
            config.showsCursor = false
            group.enter()
            SCScreenshotManager.captureImage(contentFilter: SCContentFilter(display: d, excludingWindows: []),
                                             configuration: config) { image, error in
                if let error = error { eprint("screenshot: \(error.localizedDescription)") }
                let failed = error != nil
                DispatchQueue.main.async {
                    images[i] = image
                    if failed { failures += 1 }
                    group.leave()
                }
            }
        }
        group.notify(queue: .main) {
            self.shooting = false
            // Every display failed (e.g. "Failed to find any displays or windows to capture"
            // after a display was removed, or a locked screen): refresh the display list only.
            if failures == images.count, gen == self.generation, self.active {
                self.shotTimer?.invalidate() // no more shots on the stale display list
                self.shotTimer = nil
                self.displaysStale = true
                self.recoverAfterFailure(.screenshotsFailed, "screenshots failed on every display; refreshing displays")
                return
            }
            defer { self.reschedule() }
            // Drop the frame if screens were disabled (e.g. an excluded app came up) meanwhile.
            guard !self.isExcludedNow(), self.screensEnabled, gen == self.generation, let image = sideBySide(images.compactMap { $0 }) else { return }
            let thumb = thumbnail(image)
            if shouldSkipScreenshot(prev: self.lastThumb, next: thumb, threshold: self.options.shots.sensitivity.dedupThreshold) { return }
            self.lastThumb = thumb
            let url = rawDayDir(self.options.dataDir, taken).appendingPathComponent("screens/\(screenshotName(taken))")
            self.encodeQueue.async { writeJPEG(image, to: url) }
        }
        return true
    }
}

private func sideBySide(_ images: [CGImage]) -> CGImage? {
    if images.count <= 1 { return images.first }
    let width = images.reduce(0) { $0 + $1.width }
    let height = images.map(\.height).max() ?? 0
    guard let ctx = CGContext(data: nil, width: width, height: height, bitsPerComponent: 8, bytesPerRow: 0,
                              space: CGColorSpaceCreateDeviceRGB(),
                              bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue) else { return nil }
    var x = 0
    for img in images {
        ctx.draw(img, in: CGRect(x: x, y: height - img.height, width: img.width, height: img.height))
        x += img.width
    }
    return ctx.makeImage()
}

/// 16x16 grayscale bytes for `shouldSkipScreenshot`.
private func thumbnail(_ image: CGImage) -> [UInt8] {
    var bytes = [UInt8](repeating: 0, count: 256)
    bytes.withUnsafeMutableBytes { buf in
        guard let ctx = CGContext(data: buf.baseAddress, width: 16, height: 16, bitsPerComponent: 8, bytesPerRow: 16,
                                  space: CGColorSpaceCreateDeviceGray(), bitmapInfo: CGImageAlphaInfo.none.rawValue) else { return }
        ctx.interpolationQuality = .medium
        ctx.draw(image, in: CGRect(x: 0, y: 0, width: 16, height: 16))
    }
    return bytes
}

private func writeJPEG(_ image: CGImage, to url: URL) {
    do {
        try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
    } catch {
        return eprint("screenshot: \(error.localizedDescription)")
    }
    guard let dest = CGImageDestinationCreateWithURL(url as CFURL, UTType.jpeg.identifier as CFString, 1, nil) else {
        return eprint("screenshot: cannot create \(url.lastPathComponent)")
    }
    CGImageDestinationAddImage(dest, image, [kCGImageDestinationLossyCompressionQuality: 0.6] as CFDictionary)
    if !CGImageDestinationFinalize(dest) { eprint("screenshot: cannot write \(url.lastPathComponent)") }
}
