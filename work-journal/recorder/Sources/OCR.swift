import Foundation
import Vision
import CoreGraphics
import ImageIO

/// Recognizes text in an image file. Throws if the file cannot be read.
func recognizeText(path: String) throws -> String {
    let url = URL(fileURLWithPath: path)
    guard let src = CGImageSourceCreateWithURL(url as CFURL, nil),
          let image = CGImageSourceCreateImageAtIndex(src, 0, nil) else {
        throw NSError(domain: "ocr", code: 1, userInfo: [NSLocalizedDescriptionKey: "cannot read image \(path)"])
    }
    let request = VNRecognizeTextRequest()
    request.recognitionLevel = .accurate
    request.usesLanguageCorrection = true
    try VNImageRequestHandler(cgImage: image, options: [:]).perform([request])
    let lines = (request.results ?? []).compactMap { $0.topCandidates(1).first?.string }
    return lines.joined(separator: "\n")
}

func eprint(_ s: String) {
    FileHandle.standardError.write(Data((s + "\n").utf8))
}

/// `recorder ocr <file>…`: one JSON array on stdout; per-file failures give "" and a stderr note.
func runOCR(_ files: [String]) -> Int32 {
    var out: [[String: String]] = []
    for f in files {
        do {
            out.append(["file": f, "text": try recognizeText(path: f)])
        } catch {
            eprint("ocr: \(f): \(error.localizedDescription)")
            out.append(["file": f, "text": ""])
        }
    }
    guard let data = try? JSONSerialization.data(withJSONObject: out, options: [.withoutEscapingSlashes]) else {
        eprint("ocr: cannot encode output")
        return 1
    }
    FileHandle.standardOutput.write(data)
    FileHandle.standardOutput.write(Data("\n".utf8))
    return 0
}
