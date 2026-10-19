package web

import (
	"bytes"
	"html/template"
	"net/url"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// md renders with goldmark's defaults: raw HTML is omitted and unsafe link
// schemes (javascript:, vbscript:, file:, most data:) are neutralised. No
// extensions, so bare URLs are not linkified.
var md = goldmark.New()

// renderMarkdown converts Claude-written Markdown (journals, Ask answers)
// to HTML. It is the only place in this package that produces
// template.HTML. Text the journal transcribed can steer what Claude
// writes, so nothing may make the browser contact another site: images
// are reduced to their alt text, and links (and autolinks) that are not
// relative or to the dashboard (http://<listenAddr>/…) to their text.
func renderMarkdown(src, listenAddr string) (template.HTML, error) {
	source := []byte(src)
	doc := md.Parser().Parse(text.NewReader(source))
	var unwrap, autos []ast.Node
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.Image:
			unwrap = append(unwrap, n)
		case *ast.Link:
			if !dashboardLink(string(v.Destination), listenAddr) {
				unwrap = append(unwrap, n)
			}
		case *ast.AutoLink:
			if v.AutoLinkType != ast.AutoLinkURL || !dashboardLink(string(v.URL(source)), listenAddr) {
				autos = append(autos, n)
			}
		}
		return ast.WalkContinue, nil
	})
	// Innermost first, so a nested node is moved before its parent goes.
	for i := len(unwrap) - 1; i >= 0; i-- {
		n := unwrap[i]
		parent := n.Parent()
		for c := n.FirstChild(); c != nil; {
			next := c.NextSibling()
			parent.InsertBefore(parent, n, c)
			c = next
		}
		parent.RemoveChild(parent, n)
	}
	for _, n := range autos {
		parent := n.Parent()
		parent.ReplaceChild(parent, n, ast.NewString(n.(*ast.AutoLink).Label(source)))
	}
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, source, doc); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

// dashboardLink reports whether dest is a relative link on this site or an
// absolute http link to the dashboard at listenAddr.
func dashboardLink(dest, listenAddr string) bool {
	if strings.ContainsAny(dest, "\\\x00") || strings.HasPrefix(dest, "//") {
		return false
	}
	u, err := url.Parse(dest)
	if err != nil {
		return false
	}
	if u.Scheme == "" && u.Host == "" && u.Opaque == "" && u.User == nil {
		return true
	}
	return u.Scheme == "http" && u.Host == listenAddr && u.User == nil && listenAddr != ""
}
