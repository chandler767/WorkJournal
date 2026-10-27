package web

import (
	"net/http"
	"strings"

	"workjournal/internal/wallclock"
)

const searchLimit = 50

// markSeg is a piece of a snippet; Mark segments are the matched terms.
type markSeg struct {
	Text string
	Mark bool
}

type searchHitRow struct {
	Kind, Day, When string
	Link            string
	Segs            []markSeg
}

type searchPage struct {
	Title, Q string
	Searched bool
	Hits     []searchHitRow
}

// splitMarks splits an FTS snippet on its '[' ']' match markers. The text
// stays plain strings; the template escapes every segment.
func splitMarks(snip string) []markSeg {
	var out []markSeg
	for len(snip) > 0 {
		i := strings.IndexByte(snip, '[')
		if i < 0 {
			out = append(out, markSeg{Text: snip})
			break
		}
		if i > 0 {
			out = append(out, markSeg{Text: snip[:i]})
		}
		rest := snip[i+1:]
		j := strings.IndexByte(rest, ']')
		if j < 0 {
			out = append(out, markSeg{Text: rest})
			break
		}
		out = append(out, markSeg{Text: rest[:j], Mark: true})
		snip = rest[j+1:]
	}
	return out
}

// hitLink builds the link for an index Ref, or "" when the Ref is not a
// known internal page (hour/…, day/… for a note, or meeting/<id>).
func hitLink(ref string) string {
	switch {
	case strings.HasPrefix(ref, "hour/"), strings.HasPrefix(ref, "day/"):
	case strings.HasPrefix(ref, "meeting/") && meetingIDRE.MatchString(strings.TrimPrefix(ref, "meeting/")):
	default:
		return ""
	}
	if p := "/" + ref; safeReturn(p) {
		return p
	}
	return ""
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	p := searchPage{Title: "Search", Q: q, Searched: q != ""}
	if q != "" {
		hits, err := s.Store.Search(q, searchLimit)
		if err != nil {
			http.Error(w, "search: "+err.Error(), http.StatusInternalServerError)
			return
		}
		for _, h := range hits {
			p.Hits = append(p.Hits, searchHitRow{Kind: h.Kind, Day: h.Day, When: wallclock.Clock(h.TS),
				Link: hitLink(h.Ref), Segs: splitMarks(h.Snippet)})
		}
	}
	render(w, "search", p)
}
