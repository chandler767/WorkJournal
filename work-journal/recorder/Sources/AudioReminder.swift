import Foundation
import UserNotifications

// Reminders to switch audio to Auto while the audio mode is Off. Two cases:
//
// - call: a meeting app or window is in front, or another app is using the microphone. Shown
//   once per call, cleared when the call ends or the mode leaves Off.
// - resume: activity starts after the Mac was idle (`--idle-minutes`) or asleep. Cleared after
//   `resumeShowFor`, or when the mode leaves Off.
//
// Nothing is shown while paused, asleep, excluded, or while the disk guard stops audio. A shown reminder is removed from Notification Center when it
// clears, so stale reminders do not pile up.

enum ReminderKind: String {
    case call
    case resume
}

/// Decides when to show and clear the audio reminder. Stepped every 2 s by the recorder.
struct AudioReminder {
    /// How long a resume reminder stays up.
    static let resumeShowFor: TimeInterval = 2 * 60
    /// A call reminder is not shown again within this long of the last one (a call that
    /// flickers off and on, or switching between call windows).
    static let callCooldown: TimeInterval = 10 * 60
    /// A call reminder Notification Center did not accept is tried again after this long.
    static let retryAfter: TimeInterval = 30

    enum Action: Equatable { case show(ReminderKind), clear }

    private(set) var showing: ReminderKind?
    private var shownAt: Date?
    private var lastCallShown: Date?
    private var retryCallAt: Date?
    private var wasAway = false

    /// `off`: the audio mode is Off. `allowed`: Auto could record now (not paused, asleep,
    /// excluded or stopped by the disk guard). `call`: the call signal.
    /// `away`: idle or asleep.
    mutating func step(now: Date, off: Bool, allowed: Bool, call: Bool, away: Bool) -> Action? {
        let resumed = wasAway && !away // a burst of activity after idle or sleep
        wasAway = away

        guard off && allowed else { return clear() }
        if call {
            if showing == .call { return nil }
            let cooling = lastCallShown.map { now.timeIntervalSince($0) < Self.callCooldown } ?? false
            let retryWait = retryCallAt.map { now < $0 } ?? false
            // A suppressed call still replaces a resume reminder: "Welcome back" is wrong in a call.
            if cooling || retryWait { return showing == .resume ? clear() : nil }
            lastCallShown = now
            retryCallAt = nil
            return show(.call, now)
        }
        if showing == .call { return clear() }
        if resumed { return show(.resume, now) }
        if showing == .resume, let at = shownAt, now.timeIntervalSince(at) >= Self.resumeShowFor { return clear() }
        return nil
    }

    /// Notification Center did not accept the `kind` reminder. A call reminder is not counted
    /// as shown, so a call still going is reminded again after `retryAfter`.
    mutating func notShown(_ kind: ReminderKind, now: Date) {
        guard showing == kind else { return }
        showing = nil
        shownAt = nil
        if kind == .call {
            lastCallShown = nil // it was nil or older than the cooldown before this show
            retryCallAt = now.addingTimeInterval(Self.retryAfter)
        }
    }

    private mutating func show(_ kind: ReminderKind, _ now: Date) -> Action {
        showing = kind
        shownAt = now
        return .show(kind)
    }

    private mutating func clear() -> Action? {
        guard showing != nil else { return nil }
        showing = nil
        shownAt = nil
        return .clear
    }
}

/// Title and body of the reminder notification.
func reminderText(_ kind: ReminderKind, app: String) -> (title: String, body: String) {
    switch kind {
    case .call:
        let what = app.isEmpty ? "A call" : "\(app) looks like a call"
        return ("Audio is off", "\(what), but audio recording is off. Switch to Auto to record calls and conversations.")
    case .resume:
        return ("Audio is off", "Welcome back. Audio recording is still off. Switch to Auto to record calls and conversations.")
    }
}

/// Posts and removes the reminder in Notification Center, with a "Switch to Auto" button.
/// Needs the app bundle; without one (or without permission) reminders are only logged.
final class ReminderNotifier: NSObject, UNUserNotificationCenterDelegate {
    static let id = "audio-reminder"
    private static let category = "audio-reminder"
    private static let switchAction = "switch-to-auto"

    private let center: UNUserNotificationCenter?
    private let onSwitchToAuto: () -> Void

    init(onSwitchToAuto: @escaping () -> Void) {
        self.onSwitchToAuto = onSwitchToAuto
        center = Bundle.main.bundleIdentifier == nil ? nil : UNUserNotificationCenter.current()
        super.init()
        guard let c = center else { return }
        c.delegate = self
        let action = UNNotificationAction(identifier: Self.switchAction, title: "Switch to Auto", options: [])
        c.setNotificationCategories([UNNotificationCategory(identifier: Self.category, actions: [action],
                                                            intentIdentifiers: [], options: [])])
        c.requestAuthorization(options: [.alert]) { granted, error in
            if !granted { eprint("recorder: notifications not allowed: \(error?.localizedDescription ?? "denied")") }
        }
    }

    /// `onFailure` runs on the main queue when Notification Center rejects the request.
    func show(_ kind: ReminderKind, app: String, onFailure: @escaping () -> Void) {
        let text = reminderText(kind, app: app)
        eprint("recorder: reminder (\(kind.rawValue)): \(text.body)")
        guard let c = center else { return }
        let content = UNMutableNotificationContent()
        content.title = text.title
        content.body = text.body
        content.categoryIdentifier = Self.category
        c.add(UNNotificationRequest(identifier: Self.id, content: content, trigger: nil)) { error in
            guard let e = error else { return }
            eprint("recorder: reminder: \(e.localizedDescription)")
            DispatchQueue.main.async(execute: onFailure)
        }
    }

    func clear() {
        center?.removePendingNotificationRequests(withIdentifiers: [Self.id])
        center?.removeDeliveredNotifications(withIdentifiers: [Self.id])
    }

    /// Shows the reminder even though the recorder (a menu bar app) counts as frontmost.
    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .list])
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
                                withCompletionHandler completionHandler: @escaping () -> Void) {
        if response.actionIdentifier == Self.switchAction {
            DispatchQueue.main.async(execute: onSwitchToAuto)
        }
        completionHandler()
    }
}

/// run-tests for the audio reminder state machine.
func audioReminderChecks() -> [(String, Bool)] {
    let t0 = Date(timeIntervalSince1970: 5_000_000)
    func at(_ s: TimeInterval) -> Date { t0.addingTimeInterval(s) }

    var call = AudioReminder()
    let callShow = call.step(now: t0, off: true, allowed: true, call: true, away: false)
    let callHeld = call.step(now: at(2), off: true, allowed: true, call: true, away: false)
    let callEnd = call.step(now: at(60), off: true, allowed: true, call: false, away: false)
    let soonAgain = call.step(now: at(62), off: true, allowed: true, call: true, away: false)
    _ = call.step(now: at(64), off: true, allowed: true, call: false, away: false)
    let later = call.step(now: at(700), off: true, allowed: true, call: true, away: false)

    var on = AudioReminder()
    let onCall = on.step(now: t0, off: false, allowed: true, call: true, away: false)
    let onResume = [on.step(now: at(2), off: false, allowed: true, call: false, away: true),
                    on.step(now: at(4), off: false, allowed: true, call: false, away: false)]

    var switched = AudioReminder()
    _ = switched.step(now: t0, off: true, allowed: true, call: true, away: false)
    let toAuto = switched.step(now: at(2), off: false, allowed: true, call: true, away: false)

    var paused = AudioReminder()
    let pausedCall = paused.step(now: t0, off: true, allowed: false, call: true, away: false)

    var back = AudioReminder()
    let first = back.step(now: t0, off: true, allowed: true, call: false, away: false)
    let goneIdle = back.step(now: at(2), off: true, allowed: true, call: false, away: true)
    let resumed = back.step(now: at(900), off: true, allowed: true, call: false, away: false)
    let stillUp = back.step(now: at(900 + 119), off: true, allowed: true, call: false, away: false)
    let expired = back.step(now: at(900 + 120), off: true, allowed: true, call: false, away: false)

    var callOverResume = AudioReminder()
    _ = callOverResume.step(now: t0, off: true, allowed: true, call: false, away: true)
    _ = callOverResume.step(now: at(2), off: true, allowed: true, call: false, away: false)
    let callWins = callOverResume.step(now: at(4), off: true, allowed: true, call: true, away: false)

    // A call in the cooldown still clears a resume reminder.
    var cooledResume = AudioReminder()
    _ = cooledResume.step(now: t0, off: true, allowed: true, call: true, away: false)
    _ = cooledResume.step(now: at(2), off: true, allowed: true, call: false, away: true)
    _ = cooledResume.step(now: at(60), off: true, allowed: true, call: false, away: false)
    let cooledShowing = cooledResume.showing
    let cooledCall = cooledResume.step(now: at(62), off: true, allowed: true, call: true, away: false)

    // A rejected call reminder does not use up the cooldown.
    var failed = AudioReminder()
    _ = failed.step(now: t0, off: true, allowed: true, call: true, away: false)
    failed.notShown(.call, now: at(1))
    let failedWait = failed.step(now: at(2), off: true, allowed: true, call: true, away: false)
    let failedRetry = failed.step(now: at(32), off: true, allowed: true, call: true, away: false)

    let named = reminderText(.call, app: "zoom.us").body, unnamed = reminderText(.call, app: "").body
    return [
        ("reminder: a call with audio off shows it", callShow == .show(.call)),
        ("reminder: shown once per call", callHeld == nil),
        ("reminder: clears when the call ends", callEnd == .clear),
        ("reminder: not shown again within the cooldown", soonAgain == nil),
        ("reminder: shown again after the cooldown", later == .show(.call)),
        ("reminder: nothing while audio is on or auto", onCall == nil && onResume == [nil, nil]),
        ("reminder: clears when audio leaves off", toAuto == .clear && switched.showing == nil),
        ("reminder: nothing while paused or asleep", pausedCall == nil),
        ("reminder: no resume reminder without idle first", first == nil && goneIdle == nil),
        ("reminder: activity after idle shows it", resumed == .show(.resume)),
        ("reminder: resume reminder clears after 2 min", stillUp == nil && expired == .clear),
        ("reminder: a call replaces the resume reminder", callWins == .show(.call)),
        ("reminder: a call in the cooldown clears the resume reminder",
         cooledShowing == .resume && cooledCall == .clear && cooledResume.showing == nil),
        ("reminder: a rejected call reminder is retried", failedWait == nil && failedRetry == .show(.call)),
        ("reminder: text names the call app", named.hasPrefix("zoom.us looks like a call") && unnamed.hasPrefix("A call,")),
    ]
}
