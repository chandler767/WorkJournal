package web

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
)

// md renders with goldmark's defaults: raw HTML is omitted and unsafe link
// schemes (javascript:, vbscript:, file:, most data:) are neutralised. No
// extensions, so bare URLs are not linkified.
var md = goldmark.New()

// renderMarkdown converts journal Markdown to HTML. It is the only place in
// this package that produces template.HTML.
func renderMarkdown(src string) (template.HTML, error) {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}
