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

private func appendStartEvent(dataDir: String) throws {
    let day = DateFormatter()
    day.locale = Locale(identifier: "en_US_POSIX")
    day.dateFormat = "yyyy-MM-dd"
    let dir = URL(fileURLWithPath: dataDir).appendingPathComponent("raw/\(day.string(from: Date()))")
    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    let ts = ISO8601DateFormatter()
    ts.timeZone = .current
    let line = "{\"ts\":\"\(ts.string(from: Date()))\",\"type\":\"start\"}\n"
    let file = dir.appendingPathComponent("activity.jsonl")
    if let h = try? FileHandle(forWritingTo: file) {
        defer { try? h.close() }
        try h.seekToEnd()
        try h.write(contentsOf: Data(line.utf8))
    } else {
        try Data(line.utf8).write(to: file)
    }
}

/// `recorder selftest --data-dir D`
func runSelfTest(args: [String]) -> Int32 {
    guard let i = args.firstIndex(of: "--data-dir"), i + 1 < args.count else {
        print("selftest failed: --data-dir is required")
        return 1
    }
    let dataDir = args[i + 1]
    do {
        try appendStartEvent(dataDir: dataDir)
    } catch {
        print("selftest failed: activity log: \(error.localizedDescription)")
        return 1
    }
    let img = FileManager.default.temporaryDirectory.appendingPathComponent("wj-selftest-\(UUID().uuidString).jpg")
    defer { try? FileManager.default.removeItem(at: img) }
    do {
        try renderSelftestJPEG(to: img)
        let text = try recognizeText(path: img.path)
        guard text.range(of: "selftest", options: .caseInsensitive) != nil else {
            print("selftest failed: ocr read \"\(text)\", expected \"selftest\"")
            return 1
        }
    } catch {
        print("selftest failed: ocr: \(error.localizedDescription)")
        return 1
    }
    print("selftest ok")
    return 0
}
