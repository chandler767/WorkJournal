import Foundation
import AVFoundation
import CoreMedia

/// run-tests for audio modes: VAD and turn-taking on synthetic frames, the auto state machine,
/// the writer gate, the ring buffer and its flush into chunks.
func audioModeChecks() -> [(String, Bool)] {
    vadChecks() + conversationChecks() + autoMachineChecks() + gateChecks() + ringChecks() + callChecks()
        + audioModeOptionChecks() + ringFlushChecks()
}

private func sine(_ n: Int, freq: Double, amp: Float, rate: Double = 48_000) -> [Float] {
    (0..<n).map { amp * Float(sin(2 * Double.pi * freq * Double($0) / rate)) }
}

/// Deterministic white noise in [-amp, amp].
private func noise(_ n: Int, amp: Float) -> [Float] {
    var x: UInt32 = 12345
    return (0..<n).map { _ in
        x = x &* 1_103_515_245 &+ 12345
        return amp * (Float(x >> 8) / Float(1 << 24) * 2 - 1)
    }
}

private func vadChecks() -> [(String, Bool)] {
    let frame = Int(48_000 * VAD.frameSeconds)
    func speech(_ s: [Float]) -> Bool {
        var v = VoiceActivityDetector()
        return s.withUnsafeBufferPointer { v.isSpeech($0) }
    }
    var acc = VoiceActivityDetector()
    let a = sine(1000, freq: 200, amp: 0.1), b = sine(2000, freq: 200, amp: 0.1)
    let first = a.withUnsafeBufferPointer { acc.feed($0, sampleRate: 48_000) }
    let second = b.withUnsafeBufferPointer { acc.feed($0, sampleRate: 48_000) }
    // A quiet room then a voice: the floor follows the room, the voice stands out.
    var room = VoiceActivityDetector()
    let hum = sine(frame * 100, freq: 120, amp: 0.004)
    _ = hum.withUnsafeBufferPointer { room.feed($0, sampleRate: 48_000) }
    let voiceOverRoom = sine(frame * 2, freq: 180, amp: 0.05).withUnsafeBufferPointer { room.feed($0, sampleRate: 48_000) }
    // Digital silence, then a steady room hum louder than the absolute floor, then a voice:
    // the background level must climb out of zero to the hum, so the hum is not speech.
    var after = VoiceActivityDetector()
    _ = [Float](repeating: 0, count: frame * 20).withUnsafeBufferPointer { after.feed($0, sampleRate: 48_000) }
    let floorAfterSilence = after.noiseFloor
    let humDecisions = sine(frame * 400, freq: 120, amp: 0.02).withUnsafeBufferPointer { after.feed($0, sampleRate: 48_000) }
    let speechAfterHum = sine(frame * 3, freq: 200, amp: 0.15).withUnsafeBufferPointer { after.feed($0, sampleRate: 48_000) }
    return [
        ("vad frame is 30 ms", frame == 1440),
        ("vad: silence leaves the floor at the epsilon, not 0", floorAfterSilence == VAD.noiseFloorEpsilon),
        ("vad: after silence, room hum stops counting as speech", humDecisions.suffix(100).allSatisfy { !$0 }),
        ("vad: after silence and hum, a voice is speech", speechAfterHum == [true, true, true]),
        ("vad: digital silence is not speech", !speech([Float](repeating: 0, count: frame))),
        ("vad: voiced tone is speech", speech(sine(frame, freq: 200, amp: 0.1))),
        ("vad: faint tone below the floor is not speech", !speech(sine(frame, freq: 200, amp: 0.002))),
        ("vad: loud hiss (high zero-crossing rate) is not speech", !speech(noise(frame, amp: 0.2))),
        ("vad: frames split across buffers", first.isEmpty && second.count == 2 && second == {
            var whole = VoiceActivityDetector()
            return (a + b).withUnsafeBufferPointer { whole.feed($0, sampleRate: 48_000) }
        }()),
        ("vad: a steady tone becomes background after the first frame", second == [true, false]),
        ("vad: steady room noise is not speech", room.noiseFloor < 0.006),
        ("vad: voice over room noise is speech", voiceOverRoom == [true, true]),
    ]
}

/// Frames for a repeating pattern of `speech` s of speech then `pause` s of silence.
private func pattern(speech: Double, pause: Double, total: Double) -> [Bool] {
    let f = VAD.frameSeconds
    let period = Int(((speech + pause) / f).rounded()), on = Int((speech / f).rounded())
    return (0..<Int((total / f).rounded())).map { $0 % period < on }
}

private func conversationChecks() -> [(String, Bool)] {
    func conv(_ frames: [Bool]) -> Bool {
        var d = ConversationDetector()
        frames.forEach { d.add($0) }
        return d.isConversation
    }
    var reset = ConversationDetector()
    pattern(speech: 3, pause: 1, total: 60).forEach { reset.add($0) }
    let before = reset.isConversation
    reset.reset()
    // Old talk scrolls out of the 60 s window.
    var stale = ConversationDetector()
    pattern(speech: 3, pause: 1, total: 60).forEach { stale.add($0) }
    [Bool](repeating: false, count: Int(61 / VAD.frameSeconds)).forEach { stale.add($0) }
    return [
        ("conversation: back-and-forth (3 s talk, 1 s pauses)", conv(pattern(speech: 3, pause: 1, total: 60))),
        ("conversation: unbroken speech has no turns", !conv([Bool](repeating: true, count: 2000))),
        ("conversation: sparse speech is under 40%", !conv(pattern(speech: 1, pause: 4, total: 60))),
        ("conversation: 0.1 s gaps are not turn pauses", !conv(pattern(speech: 2, pause: 0.1, total: 60))),
        ("conversation: 3 s pauses are too long for turns", !conv(pattern(speech: 2.5, pause: 3, total: 60))),
        ("conversation: 20 s of talk is under 40% of the minute", !conv(pattern(speech: 3, pause: 1, total: 20))),
        ("conversation: 40% speech with turns", conv(pattern(speech: 1.2, pause: 1.8, total: 60))),
        ("conversation: reset forgets", before && !reset.isConversation),
        ("conversation: old talk leaves the window", !stale.isConversation),
        ("conversation: turn count", {
            var d = ConversationDetector()
            pattern(speech: 3, pause: 1, total: 19).forEach { d.add($0) }
            return d.turnPauses == 4 // 5 talk bursts, 4 pauses between them
        }()),
    ]
}

private func autoMachineChecks() -> [(String, Bool)] {
    let t0 = Date(timeIntervalSince1970: 3_000_000)
    func at(_ s: TimeInterval) -> Date { t0.addingTimeInterval(s) }

    var call = AutoAudio()
    let startCall = call.step(now: t0, call: true, conversation: false)
    let stillCall = call.step(now: at(2), call: true, conversation: false)

    var talk = AutoAudio()
    let startTalk = talk.step(now: t0, call: false, conversation: true)
    let talkPhase = talk.phase
    let quiet = talk.step(now: at(10), call: false, conversation: false)
    let notYet = talk.step(now: at(10 + 179), call: false, conversation: false)
    let stop = talk.step(now: at(10 + 180), call: false, conversation: false)
    let afterStop = talk.phase

    var back = AutoAudio()
    _ = back.step(now: t0, call: false, conversation: true)
    _ = back.step(now: at(10), call: false, conversation: false)
    _ = back.step(now: at(100), call: true, conversation: false) // signal back: quiet timer resets
    let held = back.step(now: at(200), call: false, conversation: false)
    let heldStill = back.step(now: at(200 + 179), call: false, conversation: false)
    let heldStop = back.step(now: at(200 + 180), call: false, conversation: false)

    var both = AutoAudio()
    let startBoth = both.step(now: t0, call: true, conversation: true)

    var idle = AutoAudio()
    let nothing = idle.step(now: t0, call: false, conversation: false)

    var paused = AutoAudio()
    _ = paused.step(now: t0, call: true, conversation: false)
    paused.reset()

    return [
        ("auto: call starts recording", startCall == .start(.call) && call.phase == .recording(.call)),
        ("auto: no repeat start while recording", stillCall == nil),
        ("auto: conversation starts recording", startTalk == .start(.inPerson) && talkPhase == .recording(.inPerson)),
        ("auto: quiet does not stop at once", quiet == nil && notYet == nil),
        ("auto: stops 180 s after both signals end", stop == .stop && afterStop == .listening),
        ("auto: a returning signal restarts the quiet timer", held == nil && heldStill == nil && heldStop == .stop),
        ("auto: call wins as the reason", startBoth == .start(.call)),
        ("auto: no signal, no start", nothing == nil && idle.phase == .listening),
        ("auto: reset (pause, mode switch) returns to listening", paused.phase == .listening),
        ("auto: stop delay is 180 s", AutoAudio.stopAfter == 180),
    ]
}

private func gateChecks() -> [(String, Bool)] {
    [
        ("gate: on writes", audioGate(mode: .on, allowed: true, autoRecording: false) == .write),
        ("gate: off never records", audioGate(mode: .off, allowed: true, autoRecording: true) == .off),
        ("gate: auto listening buffers", audioGate(mode: .auto, allowed: true, autoRecording: false) == .buffer),
        ("gate: auto recording writes", audioGate(mode: .auto, allowed: true, autoRecording: true) == .write),
        ("gate: pause/sleep/disk stops every mode",
         [AudioMode.on, .off, .auto].allSatisfy { audioGate(mode: $0, allowed: false, autoRecording: true) == .off }),
        ("gate: mic runs unless off", micWanted(.write) && micWanted(.buffer) && !micWanted(.off)),
    ]
}

private func ringChecks() -> [(String, Bool)] {
    let t0 = Date(timeIntervalSince1970: 4_000_000)
    var ring = AudioRing()
    let second = [Int16](repeating: 1, count: 48_000)
    for i in 0..<200 {
        ring.append(RingEntry(wall: t0.addingTimeInterval(Double(i)), pts: CMTime(value: Int64(i) * 48_000, timescale: 48_000),
                              sampleRate: 48_000, samples: second))
    }
    let bytes = ring.byteCount
    let entries = ring.drain()
    let walls = entries.map(\.wall)
    let floats: [Float] = [0.5, -1.5, 1.0, 0]
    let pcm = floats.withUnsafeBufferPointer { int16Samples($0, muted: false) }
    let muted = floats.withUnsafeBufferPointer { int16Samples($0, muted: true) }
    return [
        ("ring keeps the last 120 s", entries.count == 121 && walls.first == t0.addingTimeInterval(79)
            && walls.last == t0.addingTimeInterval(199)),
        ("ring drains oldest first", walls == walls.sorted()),
        ("ring is empty after drain", ring.drain().isEmpty && ring.byteCount == 0),
        ("ring of 120 s mono 48 kHz is ~11.6 MB per track", bytes == 121 * 48_000 * 2),
        ("ring PCM is clipped 16-bit", pcm == [16384, -32767, 32767, 0]),
        ("muted ring samples are silence", muted == [0, 0, 0, 0]),
    ]
}

private func callChecks() -> [(String, Bool)] {
    let me: pid_t = 100
    func p(_ pid: pid_t, _ bundle: String, _ running: Bool) -> AudioInputProcess {
        AudioInputProcess(pid: pid, bundle: bundle, runningInput: running)
    }
    return [
        ("call: another app using the mic", otherInputActive([p(me, "local.workjournal.recorder", true), p(7, "us.zoom.xos", true)], ownPID: me)),
        ("call: the triggering app is reported",
         firstOtherInput([p(me, "local.workjournal.recorder", true), p(3, "com.apple.replayd", true), p(9, "com.apple.Safari", false),
                          p(7, "us.zoom.xos", true)], ownPID: me)?.bundle == "us.zoom.xos"),
        ("call: label prefers the app name", CallSource(bundle: "us.zoom.xos", name: "zoom.us").label == "zoom.us"
            && CallSource(bundle: "us.zoom.xos", name: "").label == "us.zoom.xos" && CallSource(bundle: "", name: "").label == ""),
        ("call: only our own mic use", !otherInputActive([p(me, "local.workjournal.recorder", true)], ownPID: me)),
        ("call: screen capture daemon ignored", !otherInputActive([p(3, "com.apple.replayd", true)], ownPID: me)),
        ("call: idle apps ignored", !otherInputActive([p(7, "us.zoom.xos", false)], ownPID: me)),
        ("call: device fallback ignores our own engine",
         !deviceFallbackCall(runningSomewhere: true, ourEngineRunning: true)
            && deviceFallbackCall(runningSomewhere: true, ourEngineRunning: false)
            && !deviceFallbackCall(runningSomewhere: false, ourEngineRunning: false)),
    ]
}

private func audioModeOptionChecks() -> [(String, Bool)] {
    [
        ("--audio-mode defaults to on", (try? RunOptions.parse([]))?.audioMode == .on),
        ("--audio-mode auto parsed", (try? RunOptions.parse(["--audio-mode", "auto"]))?.audioMode == .auto),
        ("--audio-mode bad value rejected", (try? RunOptions.parse(["--audio-mode", "loud"])) == nil),
        ("audio mode command parsed", parseAudioModeCommand("audio_mode off") == .off
            && parseAudioModeCommand("audio_mode loud") == nil && parseAudioModeCommand("status") == nil),
    ]
}

/// Ring flush into the writer: buffered audio is written only when a start flushes it, into a
/// chunk named HHMMSS for the buffer's start, split at chunk boundaries.
private func ringFlushChecks() -> [(String, Bool)] {
    var cal = Calendar(identifier: .gregorian)
    cal.timeZone = .current
    let t = cal.date(from: DateComponents(year: 2026, month: 1, day: 2, hour: 14, minute: 3, second: 27))!
    let walls = (0..<120).map { t.addingTimeInterval(Double($0)) }
    let groups = ringFlushGroups(walls: walls, chunkMinutes: 5)
    let pure: [(String, Bool)] = [
        ("flush splits at the chunk boundary", groups == [0..<93, 93..<120]),
        ("flush of one chunk", ringFlushGroups(walls: Array(walls.prefix(10)), chunkMinutes: 5) == [0..<10]),
        ("flush of nothing", ringFlushGroups(walls: [], chunkMinutes: 5).isEmpty),
        ("exact chunk name HHMMSS", exactChunkName(start: t, track: "mic") == "140327-mic.m4a"),
        ("unique name: HHMM when free", uniqueChunkName(start: t, track: "mic", exact: false, exists: { _ in false }) == "1403-mic.m4a"),
        ("unique name: HHMMSS when HHMM is taken",
         uniqueChunkName(start: t, track: "mic", exact: false, exists: { $0 == "1403-mic.m4a" }) == "140327-mic.m4a"),
        ("unique name: suffix when both are taken",
         uniqueChunkName(start: t, track: "mic", exact: false, exists: { ["1403-mic.m4a", "140327-mic.m4a"].contains($0) })
            == "140327-1-mic.m4a"),
        ("unique name: exact skips HHMM and counts up",
         uniqueChunkName(start: t, track: "mic", exact: true, exists: { ["140327-mic.m4a", "140327-1-mic.m4a"].contains($0) })
            == "140327-2-mic.m4a"),
        ("unique name: nil when every name is taken", uniqueChunkName(start: t, track: "mic", exact: true, exists: { _ in true }) == nil),
    ]

    let dir = FileManager.default.temporaryDirectory.appendingPathComponent("wjring-\(getpid())")
    try? FileManager.default.removeItem(at: dir)
    defer { try? FileManager.default.removeItem(at: dir) }
    let audio = rawDayDir(dir, Date()).appendingPathComponent("audio")
    func chunks(_ track: String) -> [URL] {
        ((try? FileManager.default.contentsOfDirectory(at: audio, includingPropertiesForKeys: nil)) ?? [])
            .filter { $0.lastPathComponent.hasSuffix("-\(track).m4a") }.sorted { $0.path < $1.path }
    }
    func frames(_ u: URL) -> Int64 { (try? AVAudioFile(forReading: u))?.length ?? 0 }
    var pts: Int64 = 0
    func feed(_ w: AudioChunkWriter, _ n: Int) {
        for _ in 0..<n {
            if let s = toneSample(frames: 4800, at: pts) { w.append(s) }
            pts += 4800
        }
    }

    // Listening: nothing reaches disk; a start flushes the buffer, then live audio follows.
    let started = Date()
    let a = AudioChunkWriter(track: "flush", dataDir: dir, chunkMinutes: 60)
    a.setGate(.buffer, flush: false)
    feed(a, 20) // 2 s buffered
    a.sync()
    let nothingWhileListening = chunks("flush").isEmpty
    a.setGate(.write, flush: true)
    feed(a, 10) // 1 s live
    a.finish()
    let flushed = chunks("flush")
    let flushedName = flushed.first?.lastPathComponent ?? ""
    let nameOK = flushedName.count == "HHMMSS-flush.m4a".count
        && (flushedName == exactChunkName(start: started, track: "flush")
            || flushedName == exactChunkName(start: started.addingTimeInterval(-1), track: "flush"))
    let total = flushed.first.map(frames) ?? 0

    // Pause while listening clears the buffer: nothing from before the pause is ever written.
    let b = AudioChunkWriter(track: "cleared", dataDir: dir, chunkMinutes: 60)
    b.setGate(.buffer, flush: false)
    feed(b, 20)
    b.setGate(.off, flush: false)
    b.setGate(.buffer, flush: false)
    b.setGate(.write, flush: true)
    b.finish()
    let clearedNothing = chunks("cleared").isEmpty

    // Switching listening → on (no start fired) drops the buffer and writes live audio only.
    let c = AudioChunkWriter(track: "noflush", dataDir: dir, chunkMinutes: 60)
    c.setGate(.buffer, flush: false)
    feed(c, 20)
    c.setGate(.write, flush: false)
    feed(c, 10)
    c.finish()
    let live = chunks("noflush").first.map(frames) ?? 0

    // Off: samples ignored.
    let d = AudioChunkWriter(track: "off", dataDir: dir, chunkMinutes: 60)
    d.setGate(.off, flush: false)
    feed(d, 10)
    d.finish()

    // A flush never holds up the caller: gate and mute changes are queued, a repeat is free,
    // and rotate waits for at most one flush slice. Nothing buffered is lost.
    let e = AudioChunkWriter(track: "slices", dataDir: dir, chunkMinutes: 60)
    e.setGate(.buffer, flush: false)
    feed(e, 600) // 60 s buffered
    e.sync()
    let t0 = Date()
    e.setGate(.write, flush: true)
    e.setMuted(true)
    e.setMuted(false)
    e.setGate(.write, flush: false) // repeat: no-op
    let callerBlocked = Date().timeIntervalSince(t0)
    let r0 = Date()
    e.rotate()
    let rotateWait = Date().timeIntervalSince(r0)
    feed(e, 10) // 1 s live, queued behind the flush
    e.finish()
    let sliced = chunks("slices")
    let slicedFrames = sliced.reduce(Int64(0)) { $0 + frames($1) }

    // Rotate during a flush (Go's ProcessNow contract): when rotate returns, every chunk named
    // before the rotate is complete and playable, and no chunk opened afterwards is named
    // earlier than the rotate. Nothing is lost.
    let r = AudioChunkWriter(track: "rot", dataDir: dir, chunkMinutes: 60, flushSlice: 0.0005) // many slices
    r.setGate(.buffer, flush: false)
    feed(r, 1200) // a full 120 s ring
    r.sync()
    r.setGate(.write, flush: true)
    usleep(20_000) // a few slices in
    let midFlush = chunks("rot").count == 1 // the first chunk is open, not yet finished
    r.rotate()
    let rotatedAt = Date()
    let atRotate = chunks("rot")
    let allComplete = !atRotate.isEmpty && atRotate.allSatisfy { frames($0) > 0 }
    let framesAtRotate = atRotate.reduce(Int64(0)) { $0 + frames($1) }
    let names0 = Set(atRotate.map(\.lastPathComponent))
    usleep(1_100_000) // the live chunk opens in a later second than the rotate
    feed(r, 10)
    r.finish()
    let after = chunks("rot").filter { !names0.contains($0.lastPathComponent) }
    let rotSecond = Calendar.current.dateComponents([.hour, .minute, .second], from: rotatedAt)
    let rotKey = (rotSecond.hour ?? 0) * 3600 + (rotSecond.minute ?? 0) * 60 + (rotSecond.second ?? 0)
    func nameKey(_ u: URL) -> Int? {
        let digits = u.lastPathComponent.prefix { $0.isNumber }
        guard digits.count == 4 || digits.count == 6, let n = Int(digits) else { return nil }
        return digits.count == 6 ? n / 10000 * 3600 + n / 100 % 100 * 60 + n % 100 : n / 100 * 3600 + n % 100 * 60
    }
    // An HHMM name stands for its whole minute: it is not "earlier" when the rotate is in that minute.
    let laterNames = after.allSatisfy { u in
        guard let k = nameKey(u) else { return false }
        return u.lastPathComponent.prefix { $0.isNumber }.count == 4 ? k + 59 >= rotKey : k >= rotKey
    }
    let rotTotal = chunks("rot").reduce(Int64(0)) { $0 + frames($1) }

    // Existing files are never overwritten or deleted: the next free name is used.
    let g = AudioChunkWriter(track: "guard", dataDir: dir, chunkMinutes: 60)
    try? FileManager.default.createDirectory(at: audio, withIntermediateDirectories: true)
    let now = Date()
    var sentinels: [URL] = [audio.appendingPathComponent(chunkName(start: now, track: "guard"))]
    for k in 0..<3 { sentinels.append(audio.appendingPathComponent(exactChunkName(start: now.addingTimeInterval(Double(k)), track: "guard"))) }
    for u in sentinels { try? Data("keep".utf8).write(to: u) }
    feed(g, 10)
    g.finish()
    let sentinelsKept = sentinels.allSatisfy { (try? Data(contentsOf: $0)) == Data("keep".utf8) }
    let guardNew = chunks("guard").filter { !sentinels.map(\.lastPathComponent).contains($0.lastPathComponent) }

    return pure + [
        ("rotate mid-flush: every earlier chunk is complete when rotate returns", allComplete),
        ("rotate mid-flush: rotate really ran during the flush", midFlush),
        ("rotate mid-flush: the whole buffer is written before rotate returns", framesAtRotate >= 48_000 * 119),
        ("rotate mid-flush: no later chunk is named before the rotate", !after.isEmpty && laterNames),
        ("rotate mid-flush: all 121 s written", rotTotal >= 48_000 * 119 && rotTotal <= 48_000 * 124),
        ("existing chunk files are never overwritten or deleted", sentinelsKept),
        ("a taken name gets the next free one", guardNew.count == 1 && guardNew.allSatisfy { frames($0) > 0 }),
        ("flush: gate and mute changes do not block the caller", callerBlocked < 0.05),
        ("flush: rotate waits at most about one slice", rotateWait < 0.4),
        ("flush: all 61 s written across the rotated chunks",
         slicedFrames >= 48_000 * 59 && slicedFrames <= 48_000 * 63 && !sliced.isEmpty),
        ("listening writes nothing to disk", nothingWhileListening),
        ("start flushes the buffer into one chunk", flushed.count == 1),
        ("flushed chunk named HHMMSS for the buffer start", nameOK),
        ("flushed chunk holds buffer then live audio (~3 s)", total >= 48_000 * 27 / 10 && total <= 48_000 * 33 / 10),
        ("pause clears the buffer", clearedNothing),
        ("switch to on without a start drops the buffer (~1 s live)", live >= 48_000 * 8 / 10 && live <= 48_000 * 13 / 10),
        ("off writes nothing", chunks("off").isEmpty),
    ]
}

/// A ready mono Float32 48 kHz LPCM sample buffer of a 220 Hz tone at `at` (in 1/48000 s).
private func toneSample(frames: Int, at: Int64) -> CMSampleBuffer? {
    let tone = sine(frames, freq: 220, amp: 0.1)
    return tone.withUnsafeBufferPointer { makePCMSample($0, sampleRate: 48_000, pts: CMTime(value: at, timescale: 48_000)) }
}
