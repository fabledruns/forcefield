package web

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// nativeExtractor implements Extractor using the Go standard library plus
// golang.org/x/net/html. It never initiates network requests.
type nativeExtractor struct{}

// NewNativeExtractor returns a new native Go HTML extractor.
func NewNativeExtractor() Extractor {
	return &nativeExtractor{}
}

func (e *nativeExtractor) Name() string { return "native" }

// stripTags is the set of HTML element names whose subtrees are discarded
// entirely during boilerplate removal.
var stripTags = map[string]bool{
	"script":   true,
	"style":    true,
	"noscript": true,
	"nav":      true,
	"header":   true,
	"footer":   true,
	"aside":    true,
	"svg":      true,
	"iframe":   true,
	"form":     true,
}

// headingTags maps tag name to Markdown heading prefix.
var headingTags = map[string]string{
	"h1": "#",
	"h2": "##",
	"h3": "###",
	"h4": "####",
	"h5": "#####",
	"h6": "######",
}

// headingLevel maps tag name to level integer.
var headingLevel = map[string]int{
	"h1": 1, "h2": 2, "h3": 3, "h4": 4, "h5": 5, "h6": 6,
}

const (
	defaultMaxBytes = 512 * 1024 // 512 KiB truncation limit
)

// ExtractHTML parses rawHTML with charset detection, extracts clean content,
// and returns a Document. baseURL is used to resolve relative links.
// targets is currently unused by the native extractor (kept for interface compat).
// No network requests are ever made.
func (e *nativeExtractor) ExtractHTML(ctx context.Context, rawHTML string, baseURL string, targets []string) (Document, error) {
	if ctx.Err() != nil {
		return Document{}, ctx.Err()
	}

	// Parse base URL for link resolution.
	var base *url.URL
	if baseURL != "" {
		var err error
		base, err = url.Parse(baseURL)
		if err != nil {
			base = nil
		}
	}

	// Detect charset and decode to UTF-8.
	reader := bytes.NewReader([]byte(rawHTML))
	utf8Reader, err := charset.NewReader(reader, "text/html")
	if err != nil {
		// Fall back to parsing as-is.
		utf8Reader = bytes.NewReader([]byte(rawHTML))
	}

	doc, err := html.Parse(utf8Reader)
	if err != nil {
		return Document{}, fmt.Errorf("%w: html parse: %s", ErrParseFailed, err)
	}

	ex := &extractor{
		base:    base,
		baseURL: baseURL,
	}
	ex.walk(doc)

	md := ex.buf.String()
	raw := ex.rawBuf.String()

	truncated := false
	if utf8.RuneCountInString(md) > defaultMaxBytes {
		md = runeAwareTruncate(md, defaultMaxBytes)
		truncated = true
	}

	document := Document{
		URL:          baseURL,
		CanonicalURL: ex.canonical,
		Title:        ex.title,
		Description:  ex.description,
		Author:       ex.author,
		Published:    ex.published,
		Markdown:     fenceUntrusted(md),
		RawText:      raw,
		Headings:     ex.headings,
		Links:        ex.links,
		Tables:       ex.tables,
		ByteCount:    len(rawHTML),
		Truncated:    truncated,
		Backend:      "native",
	}

	return document, nil
}

// extractor accumulates content during tree walking.
type extractor struct {
	base        *url.URL
	baseURL     string
	buf         strings.Builder // Markdown output
	rawBuf      strings.Builder // plain-text output
	title       string
	description string
	author      string
	published   string
	canonical   string
	headings    []Heading
	links       []Link
	tables      []Table

	// traversal state
	inPre  bool
	depth  int
	inHead bool
}

// walk recursively processes an HTML node tree.
func (ex *extractor) walk(n *html.Node) {
	if n == nil {
		return
	}

	switch n.Type {
	case html.DocumentNode:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			ex.walk(c)
		}

	case html.ElementNode:
		tag := n.Data

		// Head elements: extract metadata, do not output content.
		if tag == "head" {
			ex.inHead = true
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				ex.walk(c)
			}
			ex.inHead = false
			return
		}

		if ex.inHead {
			ex.extractHeadMeta(n)
			return
		}

		// Boilerplate stripping.
		if stripTags[tag] {
			return
		}

		ex.renderElement(n, tag)

	case html.TextNode:
		if ex.inHead {
			return
		}
		text := cleanText(n.Data)
		if text == "" {
			return
		}
		ex.buf.WriteString(text)
		ex.rawBuf.WriteString(text)
	}
}

// extractHeadMeta reads metadata from <head> elements.
func (ex *extractor) extractHeadMeta(n *html.Node) {
	if n.Type != html.ElementNode {
		return
	}

	switch n.Data {
	case "title":
		if ex.title == "" {
			ex.title = textContent(n)
		}

	case "meta":
		name := attrVal(n, "name")
		prop := attrVal(n, "property")
		content := attrVal(n, "content")

		switch strings.ToLower(name) {
		case "description":
			if ex.description == "" {
				ex.description = content
			}
		case "author":
			if ex.author == "" {
				ex.author = content
			}
		case "article:published_time", "pubdate":
			if ex.published == "" {
				ex.published = content
			}
		}

		switch strings.ToLower(prop) {
		case "og:title":
			if ex.title == "" {
				ex.title = content
			}
		case "og:description":
			if ex.description == "" {
				ex.description = content
			}
		case "article:published_time":
			if ex.published == "" {
				ex.published = content
			}
		case "og:url":
			if ex.canonical == "" {
				ex.canonical = content
			}
		}

		// Twitter / other
		switch strings.ToLower(attrVal(n, "itemprop")) {
		case "author":
			if ex.author == "" {
				ex.author = content
			}
		case "datepublished":
			if ex.published == "" {
				ex.published = content
			}
		}

	case "link":
		rel := strings.ToLower(attrVal(n, "rel"))
		href := attrVal(n, "href")
		if rel == "canonical" && href != "" && ex.canonical == "" {
			ex.canonical = ex.resolveURL(href)
		}
	}
}

// renderElement writes Markdown for an element node.
func (ex *extractor) renderElement(n *html.Node, tag string) {
	switch tag {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		ex.renderHeading(n, tag)

	case "p", "div", "section", "article", "main":
		ex.buf.WriteString("\n")
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			ex.walk(c)
		}
		ex.buf.WriteString("\n")

	case "br":
		ex.buf.WriteString("  \n")

	case "hr":
		ex.buf.WriteString("\n---\n")

	case "ul":
		ex.renderList(n, false)

	case "ol":
		ex.renderList(n, true)

	case "li":
		// Handled inside renderList; fallback if encountered at top level.
		ex.buf.WriteString("- ")
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			ex.walk(c)
		}
		ex.buf.WriteString("\n")

	case "blockquote":
		ex.renderBlockquote(n)

	case "pre":
		ex.renderPre(n)

	case "code":
		if ex.inPre {
			// Already inside <pre>; handled by renderPre.
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				ex.walk(c)
			}
			return
		}
		code := textContent(n)
		ex.buf.WriteString("`")
		ex.buf.WriteString(code)
		ex.buf.WriteString("`")

	case "a":
		ex.renderLink(n)

	case "strong", "b":
		ex.buf.WriteString("**")
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			ex.walk(c)
		}
		ex.buf.WriteString("**")

	case "em", "i":
		ex.buf.WriteString("_")
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			ex.walk(c)
		}
		ex.buf.WriteString("_")

	case "table":
		ex.renderTable(n)

	case "thead", "tbody", "tfoot":
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			ex.walk(c)
		}

	case "tr", "th", "td":
		// Handled inside renderTable; skip orphan rows.

	case "figure", "figcaption", "caption":
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			ex.walk(c)
		}

	case "img":
		alt := attrVal(n, "alt")
		src := ex.resolveURL(attrVal(n, "src"))
		if alt != "" && src != "" {
			ex.buf.WriteString(fmt.Sprintf("![%s](%s)", alt, src))
		} else if alt != "" {
			ex.buf.WriteString(alt)
		}

	case "details":
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			ex.walk(c)
		}

	case "summary":
		ex.buf.WriteString("\n**")
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			ex.walk(c)
		}
		ex.buf.WriteString("**\n")

	default:
		// Generic: recurse into children.
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			ex.walk(c)
		}
	}
}

func (ex *extractor) renderHeading(n *html.Node, tag string) {
	prefix := headingTags[tag]
	text := textContent(n)
	if text == "" {
		return
	}
	ex.buf.WriteString("\n")
	ex.buf.WriteString(prefix)
	ex.buf.WriteString(" ")
	ex.buf.WriteString(text)
	ex.buf.WriteString("\n")
	ex.rawBuf.WriteString(text + "\n")

	ex.headings = append(ex.headings, Heading{
		Level: headingLevel[tag],
		Text:  text,
	})
}

func (ex *extractor) renderList(n *html.Node, ordered bool) {
	ex.buf.WriteString("\n")
	counter := 1
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "li" {
			if ordered {
				ex.buf.WriteString(fmt.Sprintf("%d. ", counter))
				counter++
			} else {
				ex.buf.WriteString("- ")
			}
			for ic := c.FirstChild; ic != nil; ic = ic.NextSibling {
				ex.walk(ic)
			}
			ex.buf.WriteString("\n")
		}
	}
	ex.buf.WriteString("\n")
}

func (ex *extractor) renderBlockquote(n *html.Node) {
	var inner strings.Builder
	save := ex.buf
	ex.buf = inner
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		ex.walk(c)
	}
	ex.buf = save

	lines := strings.Split(strings.TrimRight(inner.String(), "\n"), "\n")
	ex.buf.WriteString("\n")
	for _, line := range lines {
		ex.buf.WriteString("> ")
		ex.buf.WriteString(line)
		ex.buf.WriteString("\n")
	}
	ex.buf.WriteString("\n")
}

func (ex *extractor) renderPre(n *html.Node) {
	ex.inPre = true
	lang := ""
	// Try to detect language from class on <code> child.
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "code" {
			class := attrVal(c, "class")
			for _, part := range strings.Fields(class) {
				if strings.HasPrefix(part, "language-") {
					lang = strings.TrimPrefix(part, "language-")
					break
				}
			}
			break
		}
	}

	var code strings.Builder
	save := ex.buf
	ex.buf = code
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		ex.walk(c)
	}
	ex.buf = save
	ex.inPre = false

	ex.buf.WriteString("\n```")
	ex.buf.WriteString(lang)
	ex.buf.WriteString("\n")
	ex.buf.WriteString(code.String())
	if !strings.HasSuffix(code.String(), "\n") {
		ex.buf.WriteString("\n")
	}
	ex.buf.WriteString("```\n")
}

func (ex *extractor) addLink(text, absURL string) {
	if absURL == "" || text == "" {
		return
	}
	for _, l := range ex.links {
		if l.URL == absURL && l.Text == text {
			return
		}
	}
	ex.links = append(ex.links, Link{Text: text, URL: absURL})
}

func (ex *extractor) extractLinksFromNode(n *html.Node) {
	if n == nil {
		return
	}
	if n.Type == html.ElementNode && n.Data == "a" {
		href := attrVal(n, "href")
		text := strings.TrimSpace(textContent(n))
		absURL := ex.resolveURL(href)
		if absURL != "" && text != "" {
			ex.addLink(text, absURL)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		ex.extractLinksFromNode(c)
	}
}

func (ex *extractor) renderLink(n *html.Node) {
	href := attrVal(n, "href")
	text := textContent(n)

	absURL := ex.resolveURL(href)

	if absURL != "" && text != "" {
		ex.buf.WriteString(fmt.Sprintf("[%s](%s)", text, absURL))
		ex.addLink(strings.TrimSpace(text), absURL)
	} else {
		// Render text only.
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			ex.walk(c)
		}
	}
}

func (ex *extractor) renderTable(n *html.Node) {
	var headers []string
	var rows [][]string

	// Collect all rows from thead / tbody / tfoot / direct tr children.
	var collectRows func(node *html.Node)
	inHeader := false

	collectRows = func(node *html.Node) {
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			switch c.Data {
			case "thead":
				inHeader = true
				collectRows(c)
				inHeader = false
			case "tbody", "tfoot":
				collectRows(c)
			case "tr":
				var row []string
				isHeaderRow := inHeader
				for td := c.FirstChild; td != nil; td = td.NextSibling {
					if td.Type == html.ElementNode && (td.Data == "th" || td.Data == "td") {
						if td.Data == "th" {
							isHeaderRow = true
						}
						ex.extractLinksFromNode(td)
						row = append(row, textContent(td))
					}
				}
				if isHeaderRow && len(headers) == 0 {
					headers = row
				} else {
					rows = append(rows, row)
				}
			}
		}
	}
	collectRows(n)

	if len(headers) == 0 && len(rows) == 0 {
		return
	}

	// If no headers found, promote first row.
	if len(headers) == 0 && len(rows) > 0 {
		headers = rows[0]
		rows = rows[1:]
	}

	// Store table structure.
	ex.tables = append(ex.tables, Table{Headers: headers, Rows: rows})

	// Render as Markdown table.
	ex.buf.WriteString("\n")
	ex.buf.WriteString("| ")
	ex.buf.WriteString(strings.Join(headers, " | "))
	ex.buf.WriteString(" |\n")
	ex.buf.WriteString("|")
	for range headers {
		ex.buf.WriteString("---|")
	}
	ex.buf.WriteString("\n")
	for _, row := range rows {
		// Pad row to header width.
		for len(row) < len(headers) {
			row = append(row, "")
		}
		ex.buf.WriteString("| ")
		ex.buf.WriteString(strings.Join(row[:len(headers)], " | "))
		ex.buf.WriteString(" |\n")
	}
	ex.buf.WriteString("\n")
}

// resolveURL converts href to an absolute URL using the base URL.
func (ex *extractor) resolveURL(href string) string {
	if href == "" {
		return ""
	}
	// Reject javascript: and data: URLs.
	lower := strings.ToLower(strings.TrimSpace(href))
	if strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "data:") {
		return ""
	}
	if ex.base == nil {
		return href
	}
	ref, err := url.Parse(href)
	if err != nil {
		return href
	}
	abs := ex.base.ResolveReference(ref)
	return abs.String()
}

// textContent returns concatenated text of all descendant text nodes.
func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}

// cleanText normalises whitespace in text nodes.
func cleanText(s string) string {
	// Collapse runs of whitespace but preserve single spaces.
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == '\t' || r == '\r' || r == '\n' {
			r = ' '
		}
		if r == ' ' {
			if !prevSpace {
				b.WriteRune(r)
			}
			prevSpace = true
		} else {
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return b.String()
}

// attrVal returns the value of an attribute by name, or empty string.
func attrVal(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// runeAwareTruncate truncates s to at most maxRunes runes.
func runeAwareTruncate(s string, maxRunes int) string {
	count := 0
	for i := range s {
		if count >= maxRunes {
			return s[:i]
		}
		count++
	}
	return s
}

// fenceUntrusted wraps content to signal it comes from an untrusted source.
// This prevents prompt injection via crafted web content.
func fenceUntrusted(content string) string {
	const (
		startFence = "<!-- BEGIN EXTRACTED WEB CONTENT (untrusted) -->\n"
		endFence   = "\n<!-- END EXTRACTED WEB CONTENT -->"
	)
	return startFence + content + endFence
}
