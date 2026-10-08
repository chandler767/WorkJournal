package web

import "strings"

// codeSeg is a piece of note text; Code segments were `backticked`.
type codeSeg struct {
	Text string
	Code bool
}

// codeSegs splits s on paired backticks so the template can wrap code spans
// in <code>. Text stays plain strings; the template escapes every segment.
// An unpaired backtick is kept as literal text.
func codeSegs(s string) []codeSeg {
	var out []codeSeg
	for len(s) > 0 {
		i := strings.IndexByte(s, '`')
		if i < 0 {
			out = append(out, codeSeg{Text: s})
			break
		}
		j := strings.IndexByte(s[i+1:], '`')
		if j < 0 {
			out = append(out, codeSeg{Text: s})
			break
		}
		if i > 0 {
			out = append(out, codeSeg{Text: s[:i]})
		}
		if j > 0 {
			out = append(out, codeSeg{Text: s[i+1 : i+1+j], Code: true})
		}
		s = s[i+j+2:]
	}
	return out
}
