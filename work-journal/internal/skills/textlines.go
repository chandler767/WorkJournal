package skills

import (
	"strings"
	"unicode/utf8"
)

// LineMax bounds one text entry of a skill input in bytes. JSON escaping at
// most doubles it, which keeps every input line under the 2000 characters
// Claude's Read tool shows of a line.
const LineMax = 900

// TextLines splits text into its lines (trailing blank lines dropped), and
// any line over LineMax bytes into several (see SplitText). Rejoining with
// "\n" restores text, except that a split line's pieces are rejoined with a
// space. An empty text gives an empty slice.
func TextLines(text string) []string {
	out := []string{}
	text = strings.TrimRight(text, "\n ")
	if strings.TrimSpace(text) == "" {
		return out
	}
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimRight(l, " \r")
		if len(l) <= LineMax {
			out = append(out, l)
			continue
		}
		out = append(out, SplitText(l, LineMax)...)
	}
	return out
}

// SplitText splits t into pieces of at most max bytes, at a line break when
// there is one, else a space, else a rune boundary. Pieces are trimmed;
// blank ones are dropped.
func SplitText(t string, max int) []string {
	var out []string
	for len(t) > max {
		cut := CutUTF8(t, max)
		if i := strings.LastIndexByte(cut, '\n'); i > 0 {
			cut = cut[:i]
		} else if i := strings.LastIndexByte(cut, ' '); i > 0 {
			cut = cut[:i]
		}
		if c := strings.TrimSpace(cut); c != "" {
			out = append(out, c)
		}
		t = strings.TrimLeft(t[len(cut):], "\n ")
	}
	if t = strings.TrimSpace(t); t != "" {
		out = append(out, t)
	}
	return out
}

// CutUTF8 is the longest prefix of s of at most n bytes that ends on a rune
// boundary.
func CutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
