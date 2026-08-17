package tui

import (
	"strings"
	"sync"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
)

// renderMarkdown renders complete Markdown while a response is streaming.
// The unfinished tail remains plain text until it is structurally complete,
// which avoids flickering partial emphasis and malformed code blocks.
func renderMarkdown(markdown string, width int, streaming bool) string {
	stable, tail := completeMarkdown(markdown, streaming)
	rendered := renderCompleteMarkdown(preserveAsciiDiagrams(stable), width)
	if tail == "" {
		return rendered
	}

	tail = messageBodyStyle.Width(width).Render(tail)
	if rendered == "" {
		return tail
	}
	return strings.TrimRight(rendered, "\n") + "\n" + tail
}

var (
	rendererMu    sync.Mutex
	rendererCache = map[int]*glamour.TermRenderer{}
)

// termRenderer returns a cached glamour renderer for the given wrap width.
// Building a TermRenderer is expensive (it compiles the style config and the
// chroma formatter), and it used to happen on every streamed chunk for every
// message, which burned CPU and contributed to streaming flicker. Renderers
// are only used from the Bubble Tea update loop, but the mutex keeps the
// cache safe for tests and any future concurrent callers.
func termRenderer(width int) (*glamour.TermRenderer, error) {
	rendererMu.Lock()
	defer rendererMu.Unlock()

	if r, ok := rendererCache[width]; ok {
		return r, nil
	}

	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(forcefieldStyleConfig()),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil, err
	}
	rendererCache[width] = r
	return r, nil
}

// forcefieldStyleConfig derives Forcefield's Markdown style from glamour's
// dark styleset with the block background colors removed. The stock dark
// theme paints headings and other blocks with background colors that clash
// with Forcefield's red-on-black palette (heading bars reading as red
// smears behind ASCII art that Markdown mis-parses as setext headings), so
// here text color carries the hierarchy instead.
func forcefieldStyleConfig() ansi.StyleConfig {
	cfg := styles.DarkStyleConfig
	// A nil pointer clears the background; glamour omits the SGR sequence
	// entirely, so text color carries the hierarchy instead of painted
	// bars that clash with Forcefield's red-on-black palette.
	cfg.Document.BackgroundColor = nil
	cfg.H1.BackgroundColor = nil
	cfg.H2.BackgroundColor = nil
	cfg.H3.BackgroundColor = nil
	cfg.H4.BackgroundColor = nil
	cfg.H5.BackgroundColor = nil
	cfg.H6.BackgroundColor = nil
	cfg.Code.BackgroundColor = nil
	return cfg
}

func renderCompleteMarkdown(markdown string, width int) string {
	if markdown == "" {
		return ""
	}
	if width < 1 {
		width = 1
	}

	renderer, err := termRenderer(width)
	if err != nil {
		return messageBodyStyle.Width(width).Render(markdown)
	}
	rendered, err := renderer.Render(markdown)
	if err != nil {
		return messageBodyStyle.Width(width).Render(markdown)
	}
	return strings.TrimRight(rendered, "\n")
}

func completeMarkdown(markdown string, streaming bool) (stable, tail string) {
	if !streaming {
		return markdown, ""
	}

	lastNewline := strings.LastIndex(markdown, "\n")
	if lastNewline < 0 {
		return "", markdown
	}

	stable = markdown[:lastNewline+1]
	tail = markdown[lastNewline+1:]
	lines := strings.SplitAfter(stable, "\n")
	fenceStart := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if fenceStart < 0 {
				fenceStart = i
			} else {
				fenceStart = -1
			}
		}
	}
	if fenceStart < 0 {
		return stable, tail
	}

	prefix := strings.Join(lines[:fenceStart], "")
	return prefix, strings.Join(lines[fenceStart:], "") + tail
}

// asciiDiagramFiller reports whether line consists of nothing but diagram
// connector characters, arrowheads, and whitespace (e.g. `  |   |` or
// `  -->` between box tops). Lines containing letters never qualify.
func asciiDiagramFiller(line string) bool {
	for _, r := range line {
		switch r {
		case '|', '-', '+', '/', '\\', ' ', '\t', '\r', ':', '<', '>', '^', 'v', '=':
		default:
			return false
		}
	}
	return strings.TrimSpace(line) != ""
}

// isDiagramEdge reports whether line is a pure box-drawing edge, like
// `+--------+` or `+---+---+`: made only of connector characters, and
// joining a corner (`+`, `/`, `\`) to a pipe or dash. This is what
// distinguishes ASCII flowcharts from Markdown tables, whose separator
// rows (`|---|`) have no corners and whose cells may legitimately contain
// `+` (e.g. `C++`) alongside letters.
func isDiagramEdge(line string) bool {
	if !asciiDiagramFiller(line) {
		return false
	}
	hasPipe, hasDash, hasCorner := false, false, false
	for _, r := range line {
		switch r {
		case '|':
			hasPipe = true
		case '-':
			hasDash = true
		case '+', '/', '\\':
			hasCorner = true
		}
	}
	return hasCorner && (hasPipe || hasDash)
}

// isDiagramRow reports whether line can be part of a diagram block: either
// a pure connector run or a pipe/plus-delimited row (labels inside boxes
// are fine, e.g. `| Client | --> | Server |`).
func isDiagramRow(line string) bool {
	if asciiDiagramFiller(line) {
		return true
	}
	trimmed := strings.TrimSpace(line)
	return (strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|")) ||
		(strings.HasPrefix(trimmed, "+") && strings.HasSuffix(trimmed, "+"))
}

// preserveAsciiDiagrams wraps runs of ASCII flowchart/box-drawing lines in
// code fences before Markdown parsing. Goldmark otherwise parses `| a | b |`
// lines as tables (reflowing their columns and padding, mangling the art),
// text over `---` as setext headings, and leading `-`/`+` as list items.
//
// A diagram block is a run of two or more diagram rows containing at least
// one box edge (`+----+`). Real Markdown tables are unaffected: none of
// their lines is a pure connector edge. Already-fenced code is left alone.
func preserveAsciiDiagrams(markdown string) string {
	if !strings.ContainsAny(markdown, "|+\\/") {
		return markdown
	}

	lines := strings.SplitAfter(markdown, "\n")
	var out strings.Builder
	inFence := false
	runStart, runHasEdge := -1, false

	flushRun := func(end int) {
		if runStart < 0 {
			return
		}
		run := lines[runStart:end]
		if runHasEdge && len(run) >= 2 {
			out.WriteString("```\n")
			for _, l := range run {
				out.WriteString(l)
			}
			if strings.HasSuffix(run[len(run)-1], "\n") {
				out.WriteString("```\n")
			} else {
				out.WriteString("```")
			}
		} else {
			for _, l := range run {
				out.WriteString(l)
			}
		}
		runStart, runHasEdge = -1, false
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			flushRun(i)
			out.WriteString(line)
			inFence = !inFence
			continue
		}
		if !inFence && isDiagramRow(line) {
			if runStart < 0 {
				runStart = i
			}
			if isDiagramEdge(line) {
				runHasEdge = true
			}
			continue
		}
		flushRun(i)
		out.WriteString(line)
	}
	flushRun(len(lines))

	return out.String()
}
