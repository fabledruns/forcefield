package tui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// System-output presentation (see docs/TUI.md). Slash commands emit plain
// text through command.Context.Println; this file only decides how that
// text looks in the transcript. Content bytes never change: styling is
// ANSI-only and collapse only hides rows until expanded.
//
// Shapes, distilled from internal/command/builtin:
//   - KV dashboards (/status, /usage, /context, /compact, /mcp get):
//     aligned "Key:   value" rows, coalesced into one entry.
//   - Lists (/help, /tools, /jobs, /skills, /mcp list):
//     a header line plus indented rows.
//   - Blobs (/diff, /git, /skills show): arbitrary long text.
//   - One-liners ("No background jobs.", "Run cancelled.").
//
// Blocks longer than sysCollapseLines get a clickable ▸/▾ header like
// tool and thinking blocks; blocks longer than sysAutoCollapseLines
// start collapsed so one /diff can't flood the transcript.
const (
	// sysCollapseLines is the content height above which a system block
	// becomes collapsible. Short blocks keep today's label + body shape.
	sysCollapseLines = 8
	// sysAutoCollapseLines is the content height above which a system
	// block starts collapsed. It sits above /help's row count so command
	// reference stays visible while diffs and skill bodies collapse.
	sysAutoCollapseLines = 32
)

// sysSplitLines splits content into rows without inflating the count for
// a trailing newline: "a\nb\n" is two rows, not three.
func sysSplitLines(content string) []string {
	if content == "" {
		return []string{""}
	}
	lines := strings.Split(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// systemCollapsible reports whether a system entry earns the ▸/▾ header.
func systemCollapsible(content string) bool {
	return len(sysSplitLines(content)) > sysCollapseLines
}

// systemStartCollapsed reports whether a collapsible entry starts hidden.
// Short-enough blocks render open, preserving today's behavior for
// /status, /help, and friends; only huge outputs collapse up front.
func systemStartCollapsed(content string) bool {
	return len(sysSplitLines(content)) > sysAutoCollapseLines
}

// systemExpanded reports whether a system entry shows its body: short
// blocks always do, collapsible ones follow the toggle once touched,
// and huge ones stay hidden until expanded.
func systemExpanded(e chatEntry) bool {
	if !systemCollapsible(e.Content) {
		return true
	}
	if e.SysExpanded {
		return true
	}
	return !systemStartCollapsed(e.Content)
}

// sysSection is one collapsible record inside a grouped entry: a header
// row plus its indented detail rows, parsed from content. For /agent
// each section is one agent: compact name + description up front,
// tools:/skills: hidden until expanded.
type sysSection struct {
	name    string
	active  bool
	header  string
	details []string
}

// sysBlock is one ordered chunk of a grouped entry: either plain lines
// or a single collapsible section. Order matches content exactly, so
// interstitial lines never move.
type sysBlock struct {
	lines   []string
	section *sysSection
}

// parseSysGroup recognizes record blocks: a header row (optional ●/○
// marker, single identifier token, em-dash description) followed by
// deeper-indented detail rows. Inactive /agent rows carry no marker at
// all, and the entry's first record sits one level deeper than the rest
// (an artifact of the outer "  %s" format), so details compare against
// the shallowest record indent in the entry, not their own record. It
// fails closed to nil, so anything else renders through the normal path
// with bytes intact.
func parseSysGroup(content string) []sysBlock {
	lines := sysSplitLines(content)
	type record struct {
		indent       string
		marker       string
		name         string
		lineIdx      int
		headerIndent int
	}
	var records []record
	for i, line := range lines {
		if indent, marker, name, _, ok := parseSysRecord(line); ok {
			records = append(records, record{indent: indent, marker: marker, name: name, lineIdx: i})
		}
	}
	if len(records) == 0 {
		return nil
	}
	base := len(records[0].indent)
	for _, r := range records[1:] {
		if len(r.indent) < base {
			base = len(r.indent)
		}
	}
	recordAt := make(map[int]record, len(records))
	for _, r := range records {
		recordAt[r.lineIdx] = r
	}
	var blocks []sysBlock
	var plain []string
	flushPlain := func() {
		if len(plain) > 0 {
			blocks = append(blocks, sysBlock{lines: plain})
			plain = nil
		}
	}
	var cur *sysSection
	flushSec := func() bool {
		if cur == nil {
			return true
		}
		if len(cur.details) == 0 {
			return false
		}
		sec := *cur
		blocks = append(blocks, sysBlock{section: &sec})
		cur = nil
		return true
	}
	for i, line := range lines {
		if r, ok := recordAt[i]; ok {
			if !flushSec() {
				return nil
			}
			flushPlain()
			cur = &sysSection{name: r.name, active: r.marker == "●", header: line}
			continue
		}
		if cur != nil && isSysDetail(line, base) {
			cur.details = append(cur.details, line)
			continue
		}
		if cur != nil && !flushSec() {
			return nil
		}
		plain = append(plain, line)
	}
	if cur != nil && !flushSec() {
		return nil
	}
	flushPlain()
	hasSec := false
	for _, b := range blocks {
		if b.section != nil {
			hasSec = true
		}
	}
	if !hasSec {
		return nil
	}
	return blocks
}

// parseSysRecord splits "  ● coding — description" (or marker-less
// "    legal — description") into its parts. The name must be a bare
// identifier, which keeps prose with em-dashes on the normal path.
func parseSysRecord(line string) (indent, marker, name, desc string, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	indent = line[:len(line)-len(trimmed)]
	marker = ""
	rest := trimmed
	if len([]rune(rest)) >= 2 {
		r := []rune(rest)
		if (r[0] == '●' || r[0] == '○') && r[1] == ' ' {
			marker = string(r[0])
			rest = string(r[2:])
		}
	}
	idx := strings.Index(rest, " — ")
	if idx <= 0 {
		return "", "", "", "", false
	}
	name = rest[:idx]
	if name == "" {
		return "", "", "", "", false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return "", "", "", "", false
		}
	}
	return indent, marker, name, rest[idx+len(" — "):], true
}

// isSysDetail reports whether a line belongs to the current record: blank
// never, and anything indented deeper than the entry's shallowest record
// indent (see parseSysGroup).
func isSysDetail(line string, base int) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
	return len(indent) > base
}

// sysSectionOpen reports whether a section's details show. Sections start
// closed; details hide until expanded.
func sysSectionOpen(e chatEntry, idx int) bool {
	return idx < len(e.SysSections) && e.SysSections[idx]
}

// sysHoverSection resolves a hover ID to the hovered section within entry
// i, or -1. Section regions look like "syssec:<entry>:<section>".
func sysHoverSection(hoverID string, entry int) int {
	var e, s int
	n, err := fmt.Sscanf(hoverID, "syssec:%d:%d", &e, &s)
	if err != nil || n != 2 || e != entry || s < 0 {
		return -1
	}
	return s
}

// setSysSection flips one section open or shut, growing the state slice.
func (m *model) setSysSection(entryIdx, secIdx int, open bool) {
	for len(m.entries[entryIdx].SysSections) <= secIdx {
		m.entries[entryIdx].SysSections = append(m.entries[entryIdx].SysSections, false)
	}
	m.entries[entryIdx].SysSections[secIdx] = open
}

// renderSystemEx draws one system entry like renderSystem, additionally
// targeting one hovered section and reporting section header row offsets
// for hit regions. sysHover is -1 when no section is hovered.
func (e chatEntry) renderSystemEx(width int, hovered bool, sysHover int) (string, []int) {
	if blocks := parseSysGroup(e.Content); blocks != nil {
		return e.renderSysGroup(width, blocks, sysHover)
	}
	return e.renderSystem(width, hovered), nil
}

// sysSectionTotal counts sections in parsed blocks.
func sysSectionTotal(blocks []sysBlock) int {
	n := 0
	for _, b := range blocks {
		if b.section != nil {
			n++
		}
	}
	return n
}

// sysSectionsEqual compares toggle state without importing slices for
// one call site.
func sysSectionsEqual(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sysGroupIndent is the shallowest record indent in a group. Section
// headers normalize to it so the rows align; details keep their deeper
// original indent.
func sysGroupIndent(blocks []sysBlock) string {
	indent := ""
	for _, b := range blocks {
		if b.section == nil {
			continue
		}
		raw, _, _, _, ok := parseSysRecord(b.section.header)
		if !ok {
			continue
		}
		if indent == "" || len(raw) < len(indent) {
			indent = raw
		}
	}
	return indent
}

// renderSysSectionHeader draws one record row: normalized indent, caret,
// active marker, name, and description. The description truncates so the
// row never wraps and hit geometry stays one row per section.
func renderSysSectionHeader(sec sysSection, indent string, open, hovered bool, width int) string {
	_, marker, name, desc, ok := parseSysRecord(sec.header)
	if !ok {
		name, desc = sec.header, ""
		marker = ""
	}
	caret := string(IconCollapsed)
	if open {
		caret = string(IconExpanded)
	}
	mark, markW := "", 0
	nameStyle := messageBodyStyle
	if sec.active {
		mark, markW = sysNameStyle.Render("● "), 2
		nameStyle = sysEmphStyle
	} else if marker == "○" {
		mark, markW = toolDetailStyle.Render("○ "), 2
	}
	desc = truncateCells(desc, max(width-lipgloss.Width(indent)-2-markW-lipgloss.Width(name)-3, 0))
	if hovered {
		plainMark := ""
		if sec.active {
			plainMark = "● "
		} else if marker == "○" {
			plainMark = "○ "
		}
		return hoverEmphasisStyle.Render(fmt.Sprintf("%s%s %s%s — %s", indent, caret, plainMark, name, desc))
	}
	return fmt.Sprintf("%s%s %s%s%s%s",
		indent, caret, mark, nameStyle.Render(name),
		toolDetailStyle.Render(" — "), toolDetailStyle.Render(desc))
}

// renderSysGroup draws parsed records: plain lines through the normal
// rules, one ▸/▾ row per section with details hidden until expanded. It
// also reports each section header's row offset for hit regions.
func (e chatEntry) renderSysGroup(width int, blocks []sysBlock, sysHover int) (string, []int) {
	indent := sysGroupIndent(blocks)
	styled := func(lines []string) string {
		return styleSystemBody(strings.Join(lines, "\n"))
	}
	var out []string
	var headerRows []int
	rows := 0
	emit := func(s string) {
		out = append(out, s)
		rows += strings.Count(s, "\n") + 1
	}
	secIdx := -1
	for _, b := range blocks {
		if b.section == nil {
			emit(styled(b.lines))
			continue
		}
		secIdx++
		open := sysSectionOpen(e, secIdx)
		headerRows = append(headerRows, rows)
		emit(renderSysSectionHeader(*b.section, indent, open, secIdx == sysHover, width))
		if open {
			emit(styled(b.section.details))
		}
	}
	block := messageBodyStyle.Width(width).Render(strings.Join(out, "\n"))
	return block, headerRows
}

// renderSystem draws one system entry: grouped record sections where
// recognized, the plain label + body for short blocks, a clickable ▸/▾
// header plus body for collapsible ones.
func (e chatEntry) renderSystem(width int, hovered bool) string {
	if blocks := parseSysGroup(e.Content); blocks != nil {
		s, _ := e.renderSysGroup(width, blocks, -1)
		return s
	}
	if !systemCollapsible(e.Content) {
		label := systemLabelStyle.Render("System")
		body := messageBodyStyle.Width(width).Render(styleSystemBody(e.Content))
		return fmt.Sprintf("%s\n%s", label, body)
	}
	if !systemExpanded(e) {
		return e.renderSystemSummary(width, hovered)
	}
	header := systemLabelStyle.Render(fmt.Sprintf("%s System", IconExpanded))
	if hovered {
		header = hoverEmphasisStyle.Render(fmt.Sprintf("%s System", IconExpanded))
	}
	body := messageBodyStyle.Width(width).Render(styleSystemBody(e.Content))
	return fmt.Sprintf("%s\n%s", header, body)
}

// renderSystemSummary draws the one-row collapsed form: caret, label,
// first content line, and row count.
func (e chatEntry) renderSystemSummary(width int, hovered bool) string {
	lines := sysSplitLines(e.Content)
	first := "output"
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			first = line
			break
		}
	}
	tail := fmt.Sprintf("(%d lines)", len(lines))
	// Fixed chrome around the summary text: caret, label, separators,
	// and the row count.
	chrome := lipgloss.Width(string(IconCollapsed)+" System ·  "+tail) + 1
	summary := truncateCells(first, max(width-chrome, 0))
	header := fmt.Sprintf("%s %s · %s %s",
		IconCollapsed,
		systemLabelStyle.Render("System"),
		toolDetailStyle.Render(summary),
		toolDetailStyle.Render(tail),
	)
	if hovered {
		header = hoverEmphasisStyle.Render(fmt.Sprintf("%s System · %s %s", IconCollapsed, summary, tail))
	}
	return header
}

// styleSystemBody applies the hierarchy pass to every content row:
// tables, diff coloring, key/value alignment, /command rows, bullets,
// headings, and inline semantics. Characters are preserved exactly; only
// ANSI styling is added.
func styleSystemBody(content string) string {
	lines := sysSplitLines(content)
	diffMode := len(lines) > 0 && strings.HasPrefix(lines[0], "diff --git ")
	tableRows, tableHeaders, tableSeps := sysTableRows(lines)
	single := len(lines) == 1
	styled := make([]string, 0, len(lines))
	for i, line := range lines {
		if tableRows[i] {
			switch {
			case tableSeps[i]:
				styled = append(styled, sysSepStyle.Render(line))
			case tableHeaders[i]:
				styled = append(styled, sysEmphStyle.Render(line))
			default:
				styled = append(styled, styleInline(line, false))
			}
			continue
		}
		styled = append(styled, styleSystemLine(line, single, diffMode))
	}
	return strings.Join(styled, "\n")
}

// styleSystemLine styles one system row outside tables. Anything
// unrecognized falls back to plain inline semantics, so unknown future
// output keeps its bytes and still reads cleanly.
func styleSystemLine(line string, single, diffMode bool) string {
	// A leading ✓ keeps its green confirmation and the remainder flows
	// through the normal rules, so "✓ Provider: ollama" still aligns.
	prefix := ""
	if indent, rest, ok := splitSystemCheck(line); ok {
		prefix = indent + sysOkStyle.Render("✓")
		line = rest
	}
	if diffMode {
		if s, ok := styleDiffLine(line); ok {
			return prefix + s
		}
	}
	if isSysSeparator(line) {
		return prefix + sysSepStyle.Render(line)
	}
	if key, value, ok := splitSystemKV(line); ok {
		return prefix + sysKeyStyle.Render(key+":") + styleInline(value, true)
	}
	if name, rest, ok := splitSystemCommand(line); ok {
		indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
		return prefix + indent + sysNameStyle.Render(name) + styleInline(rest, true)
	}
	if marker, rest, ok := splitSystemBullet(line); ok {
		indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
		return prefix + indent + toolDetailStyle.Render(marker) + styleInline(rest, false)
	}
	if isSysHeading(line) {
		return prefix + sysEmphStyle.Render(line)
	}
	return prefix + styleInline(line, single)
}

// sysTableRows prescans a block for markdown table rows: lines wrapped in
// pipes with at least one sibling. It reports per row whether it is part
// of a table, its header, or its separator.
func sysTableRows(lines []string) (rows, headers, seps []bool) {
	rows = make([]bool, len(lines))
	headers = make([]bool, len(lines))
	seps = make([]bool, len(lines))
	count := 0
	for _, line := range lines {
		if isSysTableRow(line) {
			count++
		}
	}
	if count < 2 {
		return rows, headers, seps
	}
	seen := false
	for i, line := range lines {
		if !isSysTableRow(line) {
			continue
		}
		rows[i] = true
		trimmed := strings.TrimSpace(line)
		if !seen && !isSysTableSep(trimmed) {
			headers[i] = true
			seen = true
		} else if isSysTableSep(trimmed) {
			seps[i] = true
		}
	}
	return rows, headers, seps
}

// isSysTableRow reports whether a line looks like a markdown table row.
func isSysTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return len(trimmed) >= 3 && strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|")
}

// isSysTableSep reports whether a table row is a separator
// (| --- | :--- |).
func isSysTableSep(trimmed string) bool {
	for _, r := range trimmed {
		if r != '|' && r != '-' && r != ':' && r != ' ' {
			return false
		}
	}
	return strings.Contains(trimmed, "-")
}

// isSysSeparator reports whether a line is a bare rule: three or more of
// one repeated glyph and nothing else.
func isSysSeparator(line string) bool {
	trimmed := strings.TrimSpace(line)
	if len([]rune(trimmed)) < 3 {
		return false
	}
	first := []rune(trimmed)[0]
	if !strings.ContainsRune("-─=*_·#", first) {
		return false
	}
	for _, r := range trimmed {
		if r != first {
			return false
		}
	}
	return true
}

// splitSystemCheck pulls a leading ✓ off confirmation lines, returning
// the indentation and the remainder for the normal rules. Bytes are
// preserved exactly.
func splitSystemCheck(line string) (indent, rest string, ok bool) {
	indent = line[:len(line)-len(strings.TrimLeft(line, " "))]
	trimmed := strings.TrimLeft(line, " ")
	if !strings.HasPrefix(trimmed, "✓") {
		return "", "", false
	}
	return indent, trimmed[len("✓"):], true
}

// splitSystemBullet splits "- ", "* ", "• ", and "1. " list markers,
// marker included with its trailing space so no bytes are lost.
func splitSystemBullet(line string) (marker, rest string, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	for _, m := range []string{"- ", "* ", "• "} {
		if strings.HasPrefix(trimmed, m) {
			return m, trimmed[len(m):], true
		}
	}
	// Numbered markers: digits plus "." or ")".
	i := 0
	for i < len(trimmed) && trimmed[i] >= '0' && trimmed[i] <= '9' {
		i++
	}
	if i > 0 && i+1 < len(trimmed) && (trimmed[i] == '.' || trimmed[i] == ')') && trimmed[i+1] == ' ' {
		return trimmed[:i+2], trimmed[i+2:], true
	}
	return "", "", false
}

// isSysHeading reports whether a line functions as a section heading:
// an ALLCAPS label, or a short intro line ending in a colon.
func isSysHeading(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	if strings.HasSuffix(trimmed, ":") && lipgloss.Width(trimmed) <= 64 {
		return true
	}
	runes := []rune(trimmed)
	if lipgloss.Width(trimmed) < 4 {
		return false
	}
	letters := 0
	for _, r := range runes {
		if r >= 'a' && r <= 'z' {
			return false
		}
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == ' ' {
			if r != ' ' {
				letters++
			}
			continue
		}
		return false
	}
	return letters > 0
}

// splitSystemKV splits aligned "Key:   value" rows. The key must be a
// short token without spaces or slashes and the value must start with a
// space, which keeps URLs, times, and Windows paths on the plain path.
func splitSystemKV(line string) (key, value string, ok bool) {
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return "", "", false
	}
	key, value = line[:idx], line[idx+1:]
	trimmed := strings.TrimSpace(key)
	if trimmed == "" || lipgloss.Width(trimmed) > 18 {
		return "", "", false
	}
	if strings.ContainsAny(trimmed, " \t/") {
		return "", "", false
	}
	if value != "" && !strings.HasPrefix(value, " ") {
		return "", "", false
	}
	return key, value, true
}

// splitSystemCommand splits indented "/name ..." rows into the command
// token and the rest. The token is a bare command shape (lowercase, no
// second slash or extension), so "/home/u" and "/x.go" fall through to
// the path rules instead of turning pink.
func splitSystemCommand(line string) (name, rest string, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	if !strings.HasPrefix(trimmed, "/") {
		return "", "", false
	}
	token := trimmed
	if i := strings.IndexAny(trimmed, " \t"); i >= 0 {
		token = trimmed[:i]
	}
	if len(token) < 2 || len(token) > 16 {
		return "", "", false
	}
	for _, r := range token[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return "", "", false
		}
	}
	return token, trimmed[len(token):], true
}

// sysInlineRe finds reference tokens inside system text in one scan:
// code spans, URLs, filesystem paths, timestamps, numbers, /commands,
// booleans, and status words. At one position the earliest alternative
// wins, so code beats URLs, paths beat commands ("/home/u" is a path),
// and timestamps beat numbers.
var sysInlineRe = regexp.MustCompile(
	"(?P<code>`[^`\n]+`)" +
		"|(?P<url>https?://[^\\s<>\\]]+)" +
		"|(?P<path>(?:~(?:\\/|[\\w.\\-]+\\/)[^\\s]*|\\.\\.?\\/[^\\s]*|\\/[A-Za-z0-9_.~+\\-][A-Za-z0-9_.~+\\-\\/]*(?:\\/[A-Za-z0-9_.~+\\-\\/]*|\\.[A-Za-z][A-Za-z0-9]{0,4}\\b)|[A-Za-z]:[\\\\/][^\\s]*|[\\w+~.-]*[A-Za-z][\\w+~.-]*\\.[A-Za-z][A-Za-z0-9]{0,4}\\b))" +
		"|(?P<timestamp>\\b(?:\\d{4}-\\d{2}-\\d{2}(?:[T ]\\d{2}:\\d{2}(?::\\d{2})?(?:Z|[+-]\\d{2}:?\\d{2})?)?|\\d{2}:\\d{2}:\\d{2}(?:Z|[+-]\\d{2}:?\\d{2})?)\\b)" +
		"|(?P<number>\\bv?\\d[\\d,]*(?:\\.\\d+)*(?:%|\\b))" +
		"|(?P<command>/[a-z][a-z0-9_-]*)" +
		"|(?P<bool>(?i)\\b(?:on|off|yes|no|true|false|enabled|disabled|none)\\b)" +
		"|(?P<status>(?i)\\b(?:ok|success\\w*|succeed\\w*|done|completed?|passed|failed?|failures?|errors?|denied|refused|warnings?|pending|partial|progress|running|cancelled)\\b)",
)

// styleInline applies reference-token styling to one stretch of system
// text. rich also colors status and boolean words; plain prose uses the
// basic set only, since words like "error" or "on" need structural
// context to disambiguate. Bytes are preserved exactly.
func styleInline(s string, rich bool) string {
	if s == "" {
		return ""
	}
	matches := sysInlineRe.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return messageBodyStyle.Render(s)
	}
	var b strings.Builder
	pos := 0
	for _, m := range matches {
		kind, start, end := sysMatchKind(s, m)
		if kind == sysInlineNone || (kind >= sysInlineBool && !rich) {
			continue
		}
		b.WriteString(messageBodyStyle.Render(s[pos:start]))
		b.WriteString(sysInlineStyle(s[start:end], kind))
		pos = end
	}
	b.WriteString(messageBodyStyle.Render(s[pos:]))
	return b.String()
}

// sysInlineKind ranks which named group matched. sysInlineNone means the
// match is rejected: short extension-only paths ("e.g"), trailing
// punctuation absorbed by greedy classes, or a word-class hit glued to
// identifier characters ("read-only", "srv2").
type sysInlineKind int

const (
	sysInlineNone sysInlineKind = iota
	sysInlineCode
	sysInlineURL
	sysInlinePath
	sysInlineTimestamp
	sysInlineNumber
	sysInlineCommand
	sysInlineBool
	sysInlineStatus
)

// sysInlineGroup indexes the named groups in sysInlineRe order.
var sysInlineGroup = []sysInlineKind{
	sysInlineCode, sysInlineURL, sysInlinePath, sysInlineTimestamp,
	sysInlineNumber, sysInlineCommand, sysInlineBool, sysInlineStatus,
}

// sysMatchKind resolves one regex hit to its kind and exact span,
// trimming trailing punctuation off URLs and paths. The glue check uses
// the original indices so trimmed punctuation never unrejects a hit.
func sysMatchKind(s string, m []int) (kind sysInlineKind, start, end int) {
	for i, k := range sysInlineGroup {
		start, end = m[2+i*2], m[2+i*2+1]
		if start < 0 {
			continue
		}
		text := s[start:end]
		if k == sysInlineURL || k == sysInlinePath {
			text = strings.TrimRight(text, ".,;:!?\"')>]")
			if text == "" {
				return sysInlineNone, 0, 0
			}
			end = start + len(text)
		}
		if k == sysInlinePath && !strings.ContainsAny(text, "/\\") && lipgloss.Width(text) < 5 {
			return sysInlineNone, 0, 0
		}
		if k >= sysInlineCommand && sysGlued(s, m[2+i*2], m[2+i*2+1]) {
			return sysInlineNone, 0, 0
		}
		return k, start, end
	}
	return sysInlineNone, 0, 0
}

// sysGlued reports whether a word-class match touches identifier
// characters, meaning it is part of a larger token, not a word. Dots
// are deliberately excluded: sentence-final words ("Build complete.")
// must still match, while versions stay numeric territory.
func sysGlued(s string, start, end int) bool {
	const word = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-/"
	if start > 0 && strings.ContainsRune(word, rune(s[start-1])) {
		return true
	}
	if end < len(s) && strings.ContainsRune(word, rune(s[end])) {
		return true
	}
	return false
}

// sysInlineStyle renders one accepted token. Status words map to the
// shared semantic hues; booleans read green for affirmative, muted for
// negative or absent.
func sysInlineStyle(text string, kind sysInlineKind) string {
	switch kind {
	case sysInlineCode:
		return sysEmphStyle.Render(text)
	case sysInlineURL, sysInlinePath:
		return sysRefStyle.Render(text)
	case sysInlineTimestamp:
		return toolDetailStyle.Render(text)
	case sysInlineNumber:
		return sysNumStyle.Render(text)
	case sysInlineCommand:
		return sysNameStyle.Render(text)
	case sysInlineBool:
		switch strings.ToLower(text) {
		case "on", "yes", "true", "enabled":
			return sysOkStyle.Render(text)
		default:
			return toolDetailStyle.Render(text)
		}
	case sysInlineStatus:
		switch strings.ToLower(text) {
		case "ok", "success", "successful", "succeed", "succeeded", "done", "complete", "completed", "passed", "enabled":
			return sysOkStyle.Render(text)
		case "warning", "warnings", "pending", "partial", "progress", "running":
			if strings.ToLower(text) == "running" {
				return sysNameStyle.Render(text)
			}
			return sysWarnStyle.Render(text)
		case "cancelled":
			return toolDetailStyle.Render(text)
		default:
			return sysFailStyle.Render(text)
		}
	default:
		return messageBodyStyle.Render(text)
	}
}

// styleDiffLine colors unified-diff rows. Only runs inside diffMode (see
// styleSystemBody), so markdown bullets elsewhere never turn red.
func styleDiffLine(line string) (string, bool) {
	switch {
	case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
		return toolDetailStyle.Render(line), true
	case strings.HasPrefix(line, "@@"):
		return sysHunkStyle.Render(line), true
	case strings.HasPrefix(line, "+"):
		return sysAddedStyle.Render(line), true
	case strings.HasPrefix(line, "-"):
		return sysRemovedStyle.Render(line), true
	}
	return "", false
}
