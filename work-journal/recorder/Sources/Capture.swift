import Foundation
import AppKit
import ScreenCaptureKit
import CoreMedia
import ImageIO
import UniformTypeIdentifiers

/// One `SCStream` on the main display carrying system audio to the "system" writer, plus
/// periodic `SCScreenshotManager` screenshots (main display, or all displays side by side)
/// written as `raw/<day>/screens/HHMMSS.jpg`. Control from the main thread.
final class Capture: NSObject, SCStreamOutput, SCStreamDelegate {
    /// Gate for screenshots (pause, exclusion, sleep, disk guard).
    var screensEnabled = false {
        didSet { if !screensEnabled { generation += 1 } }
    }
    /// Selects the meeting screenshot cadence.
    var meeting = false
    var onError: ((String) -> Void)?
    var onRunning: (() -> Void)?
    /// Synchronous exclusion re-check of the current frontmost app/title; true drops the frame.
    var isExcludedNow: () -> Bool = { false }

    private let options: RunOptions
    private let systemWriter: AudioChunkWriter
    private let audioQueue = DispatchQueue(label: "workjournal.capture.audio")
    private let encodeQueue = DispatchQueue(label: "workjournal.capture.jpeg")
    private var stream: SCStream?
    private var displays: [SCDisplay] = []
    private var active = false
    private var shotTimer: Timer?
    private var retryTimer: Timer?
    private var shooting = false
    private var generation = 0
    private var lastThumb: [UInt8]?

    init(options: RunOptions, systemWriter: AudioChunkWriter) {
        self.options = options
        self.systemWriter = systemWriter
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
        shotTimer = nil
        retryTimer = nil
        generation += 1
        guard let s = stream else { return done() }
        stream = nil
        s.stopCapture { _ in DispatchQueue.main.async(execute: done) }
    }

    private func startStream(content: SCShareableContent?, error: Error?) {
        guard active else { return }
        guard let content = content, let main = content.displays.first(where: { $0.displayID == CGMainDisplayID() })
                ?? content.displays.first else {
            return retry("screen capture unavailable: \(error?.localizedDescription ?? "no display") (Screen Recording permission?)")
        }
        displays = options.allDisplays ? content.displays : [main]
        let config = SCStreamConfiguration()
        config.capturesAudio = true
        config.sampleRate = 48_000
        config.channelCount = 1
        config.excludesCurrentProcessAudio = true
        // Video frames are discarded (screenshots use SCScreenshotManager); keep them tiny and rare.
        config.width = 64
        config.height = 36
        config.minimumFrameInterval = CMTime(value: 1, timescale: 1)
        let s = SCStream(filter: SCContentFilter(display: main, excludingWindows: []), configuration: config, delegate: self)
        do {
            try s.addStreamOutput(self, type: .audio, sampleHandlerQueue: audioQueue)
            try s.addStreamOutput(self, type: .screen, sampleHandlerQueue: audioQueue)
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
                    self.scheduleShot(after: 0)
                }
            }
        }
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
        if type == .audio { systemWriter.append(sampleBuffer) }
    }

    func stream(_ stream: SCStream, didStopWithError error: Error) {
        DispatchQueue.main.async {
            guard self.stream === stream else { return }
            self.stream = nil
            self.shotTimer?.invalidate()
            self.systemWriter.finish()
            self.retry("capture stopped: \(error.localizedDescription)")
        }
    }

    // MARK: Screenshots

    private func scheduleShot(after seconds: TimeInterval) {
        shotTimer?.invalidate()
        shotTimer = Timer.scheduledTimer(withTimeInterval: seconds, repeats: false) { [weak self] _ in
            guard let self = self, self.active, self.stream != nil else { return }
            self.shoot()
            self.scheduleShot(after: TimeInterval(screenshotInterval(meeting: self.meeting, self.options.policy)))
        }
    }

    private func shoot() {
        guard screensEnabled, !shooting, !displays.isEmpty else { return }
        // Title-only exclusions (a new Private/Incognito window) fire no activation notification.
        guard !isExcludedNow(), screensEnabled else { return }
        shooting = true
        let taken = Date()
        let gen = generation
        let totalWidth = displays.reduce(0) { $0 + $1.width }
        var images = [CGImage?](repeating: nil, count: displays.count)
        let group = DispatchGroup()
        for (i, d) in displays.enumerated() {
            let config = SCStreamConfiguration()
            config.width = max(1, options.width * d.width / max(1, totalWidth))
            config.height = max(1, config.width * d.height / max(1, d.width))
            config.showsCursor = false
            group.enter()
            SCScreenshotManager.captureImage(contentFilter: SCContentFilter(display: d, excludingWindows: []),
                                             configuration: config) { image, error in
                if let error = error { eprint("screenshot: \(error.localizedDescription)") }
                DispatchQueue.main.async {
                    images[i] = image
                    group.leave()
                }
            }
        }
        group.notify(queue: .main) {
            self.shooting = false
            // Drop the frame if screens were disabled (e.g. an excluded app came up) meanwhile.
            guard !self.isExcludedNow(), self.screensEnabled, gen == self.generation, let image = sideBySide(images.compactMap { $0 }) else { return }
            let thumb = thumbnail(image)
            if shouldSkipScreenshot(prev: self.lastThumb, next: thumb) { return }
            self.lastThumb = thumb
            let url = rawDayDir(self.options.dataDir, taken).appendingPathComponent("screens/\(screenshotName(taken))")
            self.encodeQueue.async { writeJPEG(image, to: url) }
        }
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
