// Package wallclock shows journal times as they read on the clock where they
// were recorded. Every key of the journal (raw chunk names, derived hours,
// meeting ids) is the recording-time wall clock, so a time is formatted in
// its own stored offset, never converted to the Mac's current zone; when
// that offset is not the Mac's, the zone is named ("10:05 BST").
package wallclock

import (
	"fmt"
	"time"
)

// known are the IANA zones tried, in order, to name a bare offset.
var known = []string{
	"America/New_York", "America/Chicago", "America/Denver", "America/Phoenix",
	"America/Los_Angeles", "America/Anchorage", "Pacific/Honolulu", "America/Halifax",
	"America/St_Johns", "America/Sao_Paulo", "Europe/London", "Europe/Paris",
	"Europe/Athens", "Europe/Moscow", "Asia/Kolkata", "Asia/Tokyo", "Asia/Seoul",
	"Australia/Sydney", "Australia/Adelaide", "Australia/Brisbane", "Australia/Perth",
	"Pacific/Auckland", "UTC",
}

// Format formats t with layout in its own offset, adding " <zone>" when
// that offset differs from the Mac's local offset at that instant.
func Format(t time.Time, layout string) string {
	return t.Format(layout) + Suffix(t)
}

// Clock is Format with "15:04".
func Clock(t time.Time) string { return Format(t, "15:04") }

// Span is "15:04–15:04" with one zone suffix when both ends share an
// offset, or each end's own suffix when they differ.
func Span(a, b time.Time) string {
	_, ao := a.Zone()
	_, bo := b.Zone()
	if ao == bo {
		return a.Format("15:04") + "–" + b.Format("15:04") + Suffix(b)
	}
	return Clock(a) + "–" + Clock(b)
}

// Suffix is "" when t's offset is the Mac's local offset at that instant,
// else " " plus the zone name.
func Suffix(t time.Time) string {
	_, off := t.Zone()
	if _, local := t.In(time.Local).Zone(); local == off {
		return ""
	}
	return " " + Zone(t)
}

// Zone names t's offset: the location's own abbreviation when it has one,
// else the abbreviation of the first known IANA zone with that offset at
// that instant, else "UTC+01:00".
func Zone(t time.Time) string {
	name, off := t.Zone()
	if isAbbrev(name) {
		return name
	}
	for _, z := range known {
		loc, err := time.LoadLocation(z)
		if err != nil {
			continue
		}
		if n, o := t.In(loc).Zone(); o == off && isAbbrev(n) {
			return n
		}
	}
	sign := '+'
	if off < 0 {
		sign, off = '-', -off
	}
	return fmt.Sprintf("UTC%c%02d:%02d", sign, off/3600, off%3600/60)
}

// isAbbrev reports whether a zone name is a letter abbreviation ("BST"),
// not empty or a numeric one ("+01").
func isAbbrev(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}
