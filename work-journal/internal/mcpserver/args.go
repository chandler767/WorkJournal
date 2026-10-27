package mcpserver

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/journal"
)

var (
	dateRE      = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	weekRE      = regexp.MustCompile(`^\d{4}-W\d{2}$`)
	meetingIDRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-\d{4}-[a-z0-9-]+$`)
)

// day resolves a date argument: YYYY-MM-DD, "today" or "yesterday".
func (j *Journal) day(name, v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "today":
		return config.DayKey(j.now()), nil
	case "yesterday":
		return config.DayKey(j.now().AddDate(0, 0, -1)), nil
	}
	if dateRE.MatchString(v) {
		if _, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
			return v, nil
		}
	}
	return "", fmt.Errorf("%s must be a date YYYY-MM-DD, today or yesterday; got %q", name, v)
}

// dayRange resolves from/to; an empty bound is replaced by def (which may
// be "", open).
func (j *Journal) dayRange(from, to, defFrom, defTo string, maxDays int) (string, string, error) {
	var err error
	if from == "" {
		from = defFrom
	} else if from, err = j.day("from", from); err != nil {
		return "", "", err
	}
	if to == "" {
		to = defTo
	} else if to, err = j.day("to", to); err != nil {
		return "", "", err
	}
	if from != "" && to != "" {
		if from > to {
			return "", "", fmt.Errorf("from %s is after to %s", from, to)
		}
		if maxDays > 0 && len(days(from, to)) > maxDays {
			return "", "", fmt.Errorf("range %s..%s is longer than %d days; narrow it", from, to, maxDays)
		}
	}
	return from, to, nil
}

// days lists the days from..to inclusive (both valid YYYY-MM-DD).
func days(from, to string) []string {
	a, _ := time.ParseInLocation("2006-01-02", from, time.Local)
	b, _ := time.ParseInLocation("2006-01-02", to, time.Local)
	var out []string
	for d := a; !d.After(b); d = d.AddDate(0, 0, 1) {
		out = append(out, config.DayKey(d))
		if len(out) > 400 {
			break
		}
	}
	return out
}

// week resolves a week argument: YYYY-Www, or a date argument naming a day
// of the week.
func (j *Journal) week(v string) (string, []string, error) {
	w := strings.ToUpper(strings.TrimSpace(v))
	if !weekRE.MatchString(w) {
		d, err := j.day("week", v)
		if err != nil {
			return "", nil, fmt.Errorf("week must be an ISO week YYYY-Www (e.g. 2026-W41), today or yesterday; got %q", v)
		}
		t, _ := time.ParseInLocation("2006-01-02", d, time.Local)
		w = config.WeekKey(t)
	}
	ds, err := journal.WeekDays(w)
	if err != nil {
		return "", nil, fmt.Errorf("week must be an ISO week YYYY-Www; %q does not exist", v)
	}
	return w, ds, nil
}

func offsetArg(name string, v int) error {
	if v < 0 {
		return fmt.Errorf("%s must be 0 or more, got %d", name, v)
	}
	return nil
}

// link is the dashboard URL of path (which starts with "/").
func (j *Journal) link(path string) string {
	return "http://" + j.Cfg.ListenAddr + path
}

// hitPath maps an index Ref to a dashboard path, or "" for an unknown one.
func hitPath(ref string) string {
	switch {
	case strings.HasPrefix(ref, "hour/"), strings.HasPrefix(ref, "day/"):
	case strings.HasPrefix(ref, "meeting/") && meetingIDRE.MatchString(strings.TrimPrefix(ref, "meeting/")):
	default:
		return ""
	}
	if strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.ContainsAny(ref, "\\ \n") {
		return ""
	}
	return "/" + ref
}

// MaxOutput caps every tool response, in bytes.
const MaxOutput = 40000

// noteRoom is kept free for the truncation note.
const noteRoom = 300

// paged joins head and items[offset:] while they fit in MaxOutput. When
// items are left over it appends a note naming the offset to call again
// with (param) — "what" names the items. A head longer than half the
// budget is cut; an item too long to fit at all is cut, so every page
// makes progress.
func paged(head string, items []string, offset int, param, what string) string {
	var b strings.Builder
	if len(head) > MaxOutput/2 {
		head = cut(head, MaxOutput/2) + "\n[… cut]"
	}
	b.WriteString(head)
	if offset > len(items) || (offset > 0 && offset == len(items)) {
		fmt.Fprintf(&b, "\n[%s=%d is past the end: there are %d %s.]\n", param, offset, len(items), what)
		return b.String()
	}
	i := offset
	for ; i < len(items); i++ {
		it := items[i] + "\n"
		room := MaxOutput - noteRoom - b.Len()
		if len(it) > room {
			if i > offset {
				break
			}
			it = cut(it, room-20) + " [… cut]\n"
		}
		b.WriteString(it)
	}
	if i < len(items) {
		fmt.Fprintf(&b, "\n[Truncated: showing %s %d–%d of %d. Call again with %s=%d for the rest.]\n", what, offset+1, i, len(items), param, i)
	}
	return b.String()
}

// cut shortens s to at most n bytes on a UTF-8 boundary.
func cut(s string, n int) string {
	if n < 0 {
		n = 0
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// firstLine is s up to its first sentence end or newline, at most n runes.
func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}

func minutes(d time.Duration) int { return int(d.Round(time.Minute) / time.Minute) }
