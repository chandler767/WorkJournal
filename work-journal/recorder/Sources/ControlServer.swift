import Foundation

/// Unix-socket control listener: one newline-terminated command per connection, one
/// newline-terminated reply. Commands are handled on the main thread; `then` runs on the
/// main thread after the reply has been written (used by `quit`).
/// Thrown by `ControlServer.start` when another process is serving the socket.
struct AnotherRecorderRunning: LocalizedError {
    var errorDescription: String? { "another recorder is running" }
}

/// Reports whether a process accepts connections on the unix socket at `path`; false when
/// the file is missing, is not a socket, or is a stale socket nobody listens on.
func socketHasListener(path: String) -> Bool {
    var addr = sockaddr_un()
    addr.sun_family = sa_family_t(AF_UNIX)
    let bytes = Array(path.utf8)
    guard bytes.count < MemoryLayout.size(ofValue: addr.sun_path) else { return false }
    withUnsafeMutableBytes(of: &addr.sun_path) { buf in
        buf.copyBytes(from: bytes)
        buf[bytes.count] = 0
    }
    let s = socket(AF_UNIX, SOCK_STREAM, 0)
    guard s >= 0 else { return false }
    defer { close(s) }
    var nosig: Int32 = 1
    setsockopt(s, SOL_SOCKET, SO_NOSIGPIPE, &nosig, socklen_t(MemoryLayout<Int32>.size))
    let r = withUnsafePointer(to: &addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
            connect(s, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
        }
    }
    return r == 0
}

final class ControlServer {
    typealias Handler = (String) -> (reply: String, then: (() -> Void)?)

    private let path: String
    private let handler: Handler
    private var fd: Int32 = -1

    init(path: String, handler: @escaping Handler) {
        self.path = path
        self.handler = handler
    }

    func start() throws {
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let bytes = Array(path.utf8)
        guard bytes.count < MemoryLayout.size(ofValue: addr.sun_path) else {
            throw RunOptionsError("socket path too long (\(bytes.count) bytes): \(path)")
        }
        withUnsafeMutableBytes(of: &addr.sun_path) { buf in
            buf.copyBytes(from: bytes)
            buf[bytes.count] = 0
        }
        // A live listener means another recorder owns the socket: refuse rather than
        // unlink it (two recorders would record everything twice). Only a stale socket
        // file from a crashed run is removed.
        if socketHasListener(path: path) { throw AnotherRecorderRunning() }
        unlink(path)
        let s = socket(AF_UNIX, SOCK_STREAM, 0)
        guard s >= 0 else { throw posixError("socket") }
        let bound = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                bind(s, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        guard bound == 0 else { close(s); throw posixError("bind \(path)") }
        chmod(path, 0o600)
        guard listen(s, 8) == 0 else { close(s); throw posixError("listen") }
        fd = s
        let thread = Thread { [weak self] in self?.acceptLoop(s) }
        thread.name = "workjournal.control"
        thread.start()
    }

    func stop() {
        guard fd >= 0 else { return }
        close(fd)
        fd = -1
        unlink(path)
    }

    private func acceptLoop(_ listener: Int32) {
        while true {
            let c = accept(listener, nil, nil)
            if c < 0 {
                if errno == EINTR { continue }
                return // listener closed
            }
            serve(c)
        }
    }

    private func serve(_ c: Int32) {
        defer { close(c) }
        var tv = timeval(tv_sec: 5, tv_usec: 0)
        setsockopt(c, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))
        var nosig: Int32 = 1
        setsockopt(c, SOL_SOCKET, SO_NOSIGPIPE, &nosig, socklen_t(MemoryLayout<Int32>.size))
        var data = [UInt8]()
        var buf = [UInt8](repeating: 0, count: 256)
        while !data.contains(0x0A) && data.count < 1024 {
            let n = read(c, &buf, buf.count)
            if n <= 0 { break }
            data.append(contentsOf: buf[0..<n])
        }
        let line = String(decoding: data.prefix { $0 != 0x0A }, as: UTF8.self)
            .trimmingCharacters(in: .whitespacesAndNewlines)
        var result: (reply: String, then: (() -> Void)?) = ("", nil)
        DispatchQueue.main.sync { result = handler(line) }
        let out = Array((result.reply + "\n").utf8)
        _ = out.withUnsafeBytes { write(c, $0.baseAddress, $0.count) }
        if let then = result.then { DispatchQueue.main.async(execute: then) }
    }

    private func posixError(_ what: String) -> Error {
        RunOptionsError("\(what): \(String(cString: strerror(errno)))")
    }
}
