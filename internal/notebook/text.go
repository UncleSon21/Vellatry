package notebook

import (
	"bytes"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Reading a document is parsing, not extraction: no model decides what a page says. What
// comes out is the text a reader sees, in the document's own order, which is what makes a
// citation checkable against the page it came from.

// MaxSource is as much text as one source may hold. A document larger than this is
// refused whole rather than silently truncated: an answer citing page 3 of a document we
// only read half of is exactly the failure the citations exist to prevent.
const MaxSource = 400_000

// ErrTooLarge is returned for a document past MaxSource.
var ErrTooLarge = errors.New("notebook: the document is too large")

// ErrNotText is returned for bytes that are not text at all.
var ErrNotText = errors.New("notebook: that file is not text")

// ErrEmpty is returned when a document holds no readable text.
var ErrEmpty = errors.New("notebook: there is no text in that document")

// Read turns a fetched or uploaded document into the text the notebook works from.
// contentType is the server's or the upload's, and is only a hint: a document that
// parses as HTML is read as HTML whatever it claims to be.
func Read(contentType string, body []byte) (string, error) {
	if len(body) > MaxSource*4 {
		return "", ErrTooLarge
	}
	body = bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF}) // a byte-order mark from Windows
	if !utf8.Valid(body) {
		return "", ErrNotText
	}
	if bytes.IndexByte(body, 0) >= 0 {
		return "", ErrNotText // PDFs, images and office documents all land here
	}
	text := string(body)
	if looksLikeHTML(contentType, text) {
		text = ReadHTML(body)
	}
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return "", ErrEmpty
	case len(text) > MaxSource:
		return "", ErrTooLarge
	}
	return text, nil
}

func looksLikeHTML(contentType, text string) bool {
	if strings.Contains(strings.ToLower(contentType), "html") {
		return true
	}
	head := text
	if len(head) > 1000 {
		head = head[:1000]
	}
	head = strings.ToLower(head)
	return strings.Contains(head, "<!doctype html") || strings.Contains(head, "<html")
}

// ReadHTML returns the visible text of an HTML document, one block element per line.
// Scripts, styles, navigation and footers are left out: they repeat on every page of a
// site and would be retrieved instead of the page's own words.
func ReadHTML(body []byte) string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return ""
	}
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Script, atom.Style, atom.Noscript, atom.Template, atom.Nav, atom.Footer, atom.Svg:
				return
			}
		}
		if n.Type == html.TextNode {
			if t := strings.Join(strings.Fields(n.Data), " "); t != "" {
				b.WriteString(t + " ")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && breaksLine(n.DataAtom) {
			b.WriteString("\n\n")
		}
	}
	walk(doc)
	var lines []string
	for _, line := range strings.Split(b.String(), "\n\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n\n")
}

func breaksLine(a atom.Atom) bool {
	switch a {
	case atom.P, atom.Div, atom.Section, atom.Article, atom.Li, atom.Tr, atom.Br, atom.Blockquote,
		atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Table, atom.Ul, atom.Ol, atom.Pre, atom.Dd, atom.Dt:
		return true
	}
	return false
}

// TitleOf returns an HTML document's title, for naming a source the team did not name.
func TitleOf(body []byte) string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return ""
	}
	var title string
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if title != "" {
			return
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.Title && n.FirstChild != nil {
			title = strings.Join(strings.Fields(n.FirstChild.Data), " ")
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return title
}
