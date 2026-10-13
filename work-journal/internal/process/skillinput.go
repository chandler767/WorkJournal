package process

import (
	"slices"
	"strings"

	"workjournal/internal/media"
	"workjournal/internal/skills"
)

// Skill inputs must stay readable by Claude's Read tool, which returns at
// most ~25k tokens per call and pages by line: an hour of screenshots can
// carry half a megabyte of OCR text, nearly all of it the same window chrome
// and page text captured again and again. So on-screen text is compacted
// (compactOCR), capped per hour or meeting (ocr_chars_per_hour), cut further
// while the whole input is over input_max_bytes (fitOCR), and written one
// record per line with no line over maxInputLine (skills.MarshalInput).

const (
	// ocrWindow is how many earlier screenshots a line must be absent from
	// to be sent again.
	ocrWindow = 10
	// ocrEntryMax bounds one OCR (or transcript) text in bytes; longer texts
	// are split into several entries. JSON escaping can at most double it,
	// which keeps every input line under maxInputLine.
	ocrEntryMax = skills.LineMax
	// maxInputLine is the longest line a skill input should have: Claude's
	// Read tool cuts lines over 2000 characters.
	maxInputLine = 2000
	// minOCRCap is the smallest OCR cap fitOCR tries before sending none.
	minOCRCap = 1000

	defaultOCRCharsPerHour = 30000
	defaultInputMaxBytes   = 70000
)

// compactOCR returns the new on-screen text of screens (in time order), at
// most limit bytes in total, split into entries of at most ocrEntryMax.
func compactOCR(screens []media.ScreenText, limit int) []string {
	return splitEntries(capOCR(newScreenText(screens), limit), ocrEntryMax)
}

// newScreenText returns, per screenshot in time order, the lines not seen
// (trimmed, ignoring case) on that screenshot already or on any of the
// previous ocrWindow screenshots; screenshots with nothing new are dropped.
// Window chrome and a page that stays open are sent once, and again only
// after being off screen for ocrWindow screenshots.
func newScreenText(screens []media.ScreenText) []string {
	ss := slices.Clone(screens)
	slices.SortStableFunc(ss, func(a, b media.ScreenText) int { return a.Time.Compare(b.Time) })
	var window []map[string]bool
	out := []string{}
	for _, sc := range ss {
		seen := map[string]bool{}
		var kept []string
		for _, l := range strings.Split(sc.Text, "\n") {
			l = strings.TrimSpace(l)
			key := strings.ToLower(l)
			if l == "" || seen[key] {
				continue
			}
			seen[key] = true
			if !slices.ContainsFunc(window, func(w map[string]bool) bool { return w[key] }) {
				kept = append(kept, l)
			}
		}
		if window = append(window, seen); len(window) > ocrWindow {
			window = window[1:]
		}
		if len(kept) > 0 {
			out = append(out, strings.Join(kept, "\n"))
		}
	}
	return out
}

// capOCR keeps texts within limit bytes in total: the most texts that fit
// when picked evenly across the list, always including the first and last.
// A first text alone over limit is cut to it.
func capOCR(texts []string, limit int) []string {
	total := 0
	for _, t := range texts {
		total += len(t)
	}
	if total <= limit || len(texts) == 0 {
		return texts
	}
	for k := len(texts) - 1; k >= 2; k-- {
		idx := evenIndexes(len(texts), k)
		sum := 0
		for _, i := range idx {
			sum += len(texts[i])
		}
		if sum <= limit {
			out := make([]string, len(idx))
			for j, i := range idx {
				out[j] = texts[i]
			}
			return out
		}
	}
	if limit <= 0 {
		return []string{}
	}
	return []string{skills.CutUTF8(texts[0], limit)}
}

// evenIndexes returns k indexes spread evenly over 0..n-1 (k ≥ 2), first
// and last included.
func evenIndexes(n, k int) []int {
	out := make([]int, 0, k)
	for j := 0; j < k; j++ {
		i := (j*(n-1) + (k-1)/2) / (k - 1)
		if len(out) == 0 || i != out[len(out)-1] {
			out = append(out, i)
		}
	}
	return out
}

// splitEntries splits texts longer than max bytes into several, at a line
// break when there is one, else a space, else a rune boundary.
func splitEntries(texts []string, max int) []string {
	out := []string{}
	for _, t := range texts {
		out = append(out, skills.SplitText(t, max)...)
	}
	return out
}

// splitLines splits transcript lines whose text is over ocrEntryMax into
// consecutive lines with the same time and speaker.
func splitLines(ls []lineInput) []lineInput {
	out := make([]lineInput, 0, len(ls))
	for _, l := range ls {
		for _, t := range skills.SplitText(l.Text, ocrEntryMax) {
			out = append(out, lineInput{T: l.T, Speaker: l.Speaker, Text: t})
		}
	}
	return out
}

func (s *SkillSummarizer) ocrCap() int {
	if s.Cfg.OCRCharsPerHour > 0 {
		return s.Cfg.OCRCharsPerHour
	}
	return defaultOCRCharsPerHour
}

func (s *SkillSummarizer) inputMax() int {
	if s.Cfg.InputMaxBytes > 0 {
		return s.Cfg.InputMaxBytes
	}
	return defaultInputMaxBytes
}

// fitOCR encodes the input set builds around screens' compacted text. While
// the encoding is over input_max_bytes the OCR cap is halved (down to none)
// before anything else would be cut; over reports an input still too large
// with no OCR at all, which is sent anyway (it is line-paged).
func (s *SkillSummarizer) fitOCR(screens []media.ScreenText, set func(ocr []string) any) (b []byte, over bool, err error) {
	fresh := newScreenText(screens)
	limit := s.ocrCap()
	for {
		ocr := []string{}
		if limit > 0 {
			ocr = splitEntries(capOCR(fresh, limit), ocrEntryMax)
		}
		b, err = skills.MarshalInput(set(ocr))
		if err != nil || len(b) <= s.inputMax() {
			return b, false, err
		}
		if limit == 0 {
			return b, true, nil
		}
		if limit /= 2; limit < minOCRCap {
			limit = 0
		}
	}
}
