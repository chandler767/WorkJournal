import Foundation
import AppKit

private func renderSelftestJPEG(to url: URL) throws {
    let w = 800, h = 240
    guard let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: w, pixelsHigh: h, bitsPerSample: 8,
                                     samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                                     colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0),
          let ctx = NSGraphicsContext(bitmapImageRep: rep) else {
        throw NSError(domain: "selftest", code: 1, userInfo: [NSLocalizedDescriptionKey: "cannot create bitmap"])
    }
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = ctx
    NSColor.white.setFill()
    NSRect(x: 0, y: 0, width: w, height: h).fill()
    let attrs: [NSAttributedString.Key: Any] = [
        .font: NSFont.systemFont(ofSize: 110, weight: .bold),
        .foregroundColor: NSColor.black,
    ]
    NSAttributedString(string: "selftest", attributes: attrs).draw(at: NSPoint(x: 40, y: 50))
    NSGraphicsContext.restoreGraphicsState()
    guard let jpeg = rep.representation(using: .jpeg, properties: [.compressionFactor: 0.9]) else {
        throw NSError(domain: "selftest", code: 2, userInfo: [NSLocalizedDescriptionKey: "cannot encode JPEG"])
    }
    try jpeg.write(to: url)
}

private func files(in dir: URL, suffix: String) -> [URL] {
    let all = (try? FileManager.default.contentsOfDirectory(at: dir, includingPropertiesForKeys: [.fileSizeKey])) ?? []
    return all.filter { $0.lastPathComponent.hasSuffix(suffix) }
}

private func nonEmpty(_ url: URL) -> Bool {
    ((try? url.resourceValues(forKeys: [.fileSizeKey]))?.fileSize ?? 0) > 0
}

/// Checks the media and activity a short capture wrote under `dataDir`; nil when all pass.
private func checkCapture(dataDir: URL, day: URL) -> String? {
    if files(in: day.appendingPathComponent("screens"), suffix: ".jpg").isEmpty {
        return "screens: no screens/*.jpg written (Screen Recording permission?)"
    }
    let audio = day.appendingPathComponent("audio")
    if !files(in: audio, suffix: "-system.m4a").contains(where: nonEmpty) {
        return "audio: no non-empty *-system.m4a written (Screen & System Audio Recording permission?)"
    }
    if !files(in: audio, suffix: "-mic.m4a").contains(where: nonEmpty) {
        return "audio: no non-empty *-mic.m4a written (Microphone permission?)"
    }
    let lines = (try? String(contentsOf: day.appendingPathComponent("activity.jsonl"), encoding: .utf8)) ?? ""
    let types = Set(lines.split(separator: "\n").compactMap { line -> String? in
        let obj = try? JSONSerialization.jsonObject(with: Data(line.utf8)) as? [String: Any]
        return obj?["type"] as? String
    })
    for want in ["start", "app"] where !types.contains(want) {
        return "activity: no \"\(want)\" event in activity.jsonl"
    }
    return nil
}

private func checkOCR() -> String? {
    let img = FileManager.default.temporaryDirectory.appendingPathComponent("wj-selftest-\(UUID().uuidString).jpg")
    defer { try? FileManager.default.removeItem(at: img) }
    do {
        try renderSelftestJPEG(to: img)
        let text = try recognizeText(path: img.path)
        if text.range(of: "selftest", options: .caseInsensitive) == nil {
            return "ocr read \"\(text)\", expected \"selftest\""
        }
    } catch {
        return "ocr: \(error.localizedDescription)"
    }
    return nil
}

/// `recorder selftest --data-dir D`: captures for 5 s into a fresh temp dir (never the data
/// dir, which may be the live one), checks the output, then runs OCR. The final line goes to
/// stdout and to `D/selftest.result` (stdout is not visible when launched through `open`).
func runSelfTest(args: [String]) -> Int32 {
    guard let i = args.firstIndex(of: "--data-dir"), i + 1 < args.count else {
        print("selftest failed: --data-dir is required")
        return 1
    }
    let dataDir = URL(fileURLWithPath: args[i + 1])
    let captureDir = FileManager.default.temporaryDirectory.appendingPathComponent("wj-selftest-\(UUID().uuidString)")
    var failure: String?
    do {
        try FileManager.default.createDirectory(at: dataDir, withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: captureDir, withIntermediateDirectories: true)
        let started = Date()
        try runSelftestCapture(dataDir: captureDir, seconds: 5)
        failure = checkCapture(dataDir: captureDir, day: rawDayDir(captureDir, started))
    } catch {
        failure = "capture: \(error.localizedDescription)"
    }
    if failure == nil { failure = checkOCR() }

    let line = failure.map { "selftest failed: \($0)" } ?? "selftest ok"
    if failure == nil {
        try? FileManager.default.removeItem(at: captureDir)
    } else {
        eprint("selftest: capture output kept at \(captureDir.path)")
    }
    print(line)
    do {
        try Data((line + "\n").utf8).write(to: dataDir.appendingPathComponent("selftest.result"))
    } catch {
        eprint("selftest: cannot write selftest.result: \(error.localizedDescription)")
    }
    return failure == nil ? 0 : 1
}
