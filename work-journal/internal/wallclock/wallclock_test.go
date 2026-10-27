package wallclock

import (
	"testing"
	"time"
)

// inNewYork makes the Mac's zone America/New_York for the test.
func inNewYork(t *testing.T) {
	t.Helper()
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	prev := time.Local
	time.Local = ny
	t.Cleanup(func() { time.Local = prev })
}

func TestFormatKeepsRecordedOffset(t *testing.T) {
	inNewYork(t)
	// A London meeting as stored: a bare +01:00 offset.
	london, err := time.Parse(time.RFC3339, "2026-10-12T10:05:00+01:00")
	if err != nil {
		t.Fatal(err)
	}
	if got := Format(london, "15:04"); got != "10:05 BST" {
		t.Errorf("London time: got %q, want 10:05 BST", got)
	}
	// Recorded in the zone the Mac is in: no suffix.
	home, _ := time.Parse(time.RFC3339, "2026-10-12T11:30:00-04:00")
	if got := Format(home, "15:04"); got != "11:30" {
		t.Errorf("same-zone time: got %q, want 11:30", got)
	}
	// Summer time in New York shown in winter: still the Mac's zone.
	summer, _ := time.Parse(time.RFC3339, "2026-07-01T09:00:00-04:00")
	if got := Format(summer, "15:04"); got != "09:00" {
		t.Errorf("summer NY time: got %q, want 09:00", got)
	}
	// No known zone has the offset: UTC±hh:mm.
	odd := time.Date(2026, 10, 12, 10, 5, 0, 0, time.FixedZone("", 3*3600+17*60))
	if got := Format(odd, "15:04"); got != "10:05 UTC+03:17" {
		t.Errorf("odd offset: got %q", got)
	}
	// A named location keeps its own abbreviation.
	paris, _ := time.LoadLocation("Europe/Paris")
	if got := Format(time.Date(2026, 10, 12, 10, 5, 0, 0, paris), "15:04:05"); got != "10:05:00 CEST" {
		t.Errorf("Paris: got %q", got)
	}
}

func TestSpan(t *testing.T) {
	inNewYork(t)
	a, _ := time.Parse(time.RFC3339, "2026-10-12T10:05:00+01:00")
	b, _ := time.Parse(time.RFC3339, "2026-10-12T10:45:00+01:00")
	if got := Span(a, b); got != "10:05–10:45 BST" {
		t.Errorf("got %q", got)
	}
	c, _ := time.Parse(time.RFC3339, "2026-10-12T11:00:00-04:00")
	d, _ := time.Parse(time.RFC3339, "2026-10-12T11:30:00-04:00")
	if got := Span(c, d); got != "11:00–11:30" {
		t.Errorf("got %q", got)
	}
	if got := Span(a, d); got != "10:05 BST–11:30" {
		t.Errorf("mixed: got %q", got)
	}
}

// A bare offset is named only when the candidate zones agree on one name
// for it at that instant.
func TestZoneNamesOnlyUnambiguousOffsets(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return time.Date(v.Year(), v.Month(), v.Day(), v.Hour(), v.Minute(), 0, 0, time.FixedZone("", func() int { _, o := v.Zone(); return o }()))
	}
	for _, c := range []struct{ ts, want string }{
		{"2026-10-12T10:05:00+01:00", "BST"},       // London only
		{"2026-12-01T10:05:00+00:00", "UTC+00:00"}, // London GMT and UTC
		{"2026-12-01T10:05:00-07:00", "MST"},       // Denver and Phoenix agree
		{"2026-07-01T10:05:00-03:00", "UTC-03:00"}, // Halifax ADT, São Paulo -03
	} {
		if got := Zone(at(c.ts)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.ts, got, c.want)
		}
	}
}
