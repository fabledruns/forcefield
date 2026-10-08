package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// sysEntry builds a system transcript entry with n body rows.
func sysEntry(n int) chatEntry {
	lines := make([]string, 0, n)
	for i := 0; i < n; i++ {
		lines = append(lines, fmt.Sprintf("row %d", i))
	}
	return chatEntry{Role: roleSystem, Content: strings.Join(lines, "\n")}
}

func TestSystemShortBlocksKeepLabelShape(t *testing.T) {
	for _, content := range []string{
		"Run cancelled.",
		"No background jobs.",
		"Agent:     coding\nProvider:  ollama",
	} {
		e := chatEntry{Role: roleSystem, Content: content}
		got := stripANSI(e.render(80, false))
		lines := strings.Split(got, "\n")
		if lines[0] != "System" {
			t.Errorf("content %q first row = %q, want plain System label", content, lines[0])
		}
		if strings.Contains(got, "▸") || strings.Contains(got, "▾") {
			t.Errorf("short content %q gained a collapse caret:\n%s", content, got)
		}
		// Styling must not eat or add characters (rows are
		// width-padded by the renderer, so compare trimmed).
		body := make([]string, 0, len(lines)-1)
		for _, r := range lines[1:] {
			body = append(body, strings.TrimRight(r, " "))
		}
		if got := strings.Join(body, "\n"); got != content {
			t.Errorf("body changed:\n got %q\nwant %q", got, content)
		}
	}
}

func TestSystemCollapsibleThreshold(t *testing.T) {
	if systemCollapsible(sysEntry(8).Content) {
		t.Error("8-line block is collapsible, want plain (threshold is 8)")
	}
	if !systemCollapsible(sysEntry(9).Content) {
		t.Error("9-line block is plain, want collapsible")
	}
}

func TestSystemMediumBlocksRenderOpen(t *testing.T) {
	e := sysEntry(12)
	if systemStartCollapsed(e.Content) {
		t.Fatal("12-line block starts collapsed, want open")
	}
	got := stripANSI(e.render(80, false))
	if !strings.HasPrefix(got, "▾ System") {
		t.Errorf("expanded header = %q, want ▾ System", strings.Split(got, "\n")[0])
	}
	for i := 0; i < 12; i++ {
		if !strings.Contains(got, fmt.Sprintf("row %d", i)) {
			t.Errorf("expanded body missing row %d:\n%s", i, got)
		}
	}
}

func TestSystemLongBlocksStartCollapsed(t *testing.T) {
	e := sysEntry(40)
	if !systemStartCollapsed(e.Content) {
		t.Fatal("40-line block starts open, want collapsed")
	}
	got := stripANSI(e.render(80, false))
	rows := strings.Split(got, "\n")
	if len(rows) != 1 {
		t.Fatalf("collapsed block is %d rows, want exactly 1:\n%s", len(rows), got)
	}
	if !strings.HasPrefix(rows[0], "▸ System · ") {
		t.Errorf("collapsed header = %q, want ▸ System · summary", rows[0])
	}
	if !strings.Contains(rows[0], "(40 lines)") {
		t.Errorf("collapsed header missing row count: %q", rows[0])
	}
	if strings.Contains(got, "row 39") {
		t.Errorf("collapsed body leaks content:\n%s", got)
	}

	e.SysExpanded = true
	got = stripANSI(e.render(80, false))
	if !strings.HasPrefix(got, "▾ System") {
		t.Errorf("expanded header = %q, want ▾ System", strings.Split(got, "\n")[0])
	}
	if !strings.Contains(got, "row 39") {
		t.Errorf("expanded body missing last row:\n%s", got)
	}
}

func TestSystemToggleTargetsMostRecent(t *testing.T) {
	m := newTestModel()
	m.entries = []chatEntry{sysEntry(40), sysEntry(12)}

	m.toggleExpandable()
	if !m.entries[1].SysExpanded {
		t.Error("ctrl+e did not expand the most recent system block")
	}
	if m.entries[0].SysExpanded {
		t.Error("ctrl+e touched the older block")
	}

	m.toggleExpandable()
	if m.entries[1].SysExpanded {
		t.Error("second ctrl+e did not collapse the block")
	}
}

func TestSystemTogglePrefersToolsByRecency(t *testing.T) {
	m := newTestModel()
	m.entries = []chatEntry{sysEntry(40)}
	m.startToolActivity(nil)
	m.entries = append(m.entries, chatEntry{Role: roleActivity, Content: "x", Tool: &toolRecord{name: "shell"}})

	m.toggleExpandable()
	if m.entries[0].SysExpanded {
		t.Error("ctrl+e hit the system block past a newer tool entry")
	}
}

func TestSystemClickToggles(t *testing.T) {
	m := newTestModel()
	m.entries = []chatEntry{sysEntry(40)}
	m.refreshTranscript()
	if len(m.spans) != 1 {
		t.Fatalf("got %d spans, want 1 for the collapsible block", len(m.spans))
	}
	y := m.headerRows() + m.spans[0].startLine - m.viewport.YOffset
	next, consumed := m.routeMouse(leftClick(3, y))
	m = next
	if !consumed {
		t.Fatal("click on the collapsed header was not consumed")
	}
	if !m.entries[0].SysExpanded {
		t.Error("click did not expand the block")
	}
	// The viewport shows the top of the expanded block; the tail lives
	// below the fold and scrolls like any transcript content.
	if !strings.Contains(stripANSI(m.viewport.View()), "row 0") {
		t.Error("viewport missing expanded content after click")
	}
	if got := stripANSI(m.entries[0].render(80, false)); !strings.Contains(got, "row 39") {
		t.Errorf("expanded entry missing last row:\n%s", got)
	}
}

func TestSystemKVStylingPreservesBytes(t *testing.T) {
	content := "Agent:     coding\nProvider:  ollama\nMessages:  3 (~100 B)"
	e := chatEntry{Role: roleSystem, Content: content}
	rows := strings.Split(stripANSI(e.render(80, false)), "\n")
	body := make([]string, 0, len(rows)-1)
	for _, r := range rows[1:] {
		body = append(body, strings.TrimRight(r, " "))
	}
	if got := strings.Join(body, "\n"); got != content {
		t.Errorf("KV body changed:\n got %q\nwant %q", got, content)
	}
}

func TestSystemKVSkipsNonPairs(t *testing.T) {
	for _, line := range []string{
		"https://example.com/x",
		"12:30 elapsed",
		`C:\repo\file.txt`,
		"no colon here",
		"this key is far too long for a label: value",
	} {
		if _, _, ok := splitSystemKV(line); ok {
			t.Errorf("line %q parsed as KV pair", line)
		}
	}
	if _, _, ok := splitSystemKV("Agent:     coding"); !ok {
		t.Error("aligned pair not parsed as KV")
	}
}

func TestSystemCommandRowsKeepNames(t *testing.T) {
	e := chatEntry{Role: roleSystem, Content: "Available commands:\n  /model [name]  Show or switch.\n  /help          List."}
	got := stripANSI(e.render(80, false))
	for _, want := range []string{"/model", "/help", "Available commands:"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered block missing %q:\n%s", want, got)
		}
	}
}

func TestSystemDiffColorsStayInDiffs(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	diff := "diff --git a/x b/x\n--- a/x\n+++ b/x\n+added\n-removed\n context"
	raw := styleSystemLine("+added", false, true)
	plain := styleSystemLine("- item", false, false)
	if stripANSI(raw) != "+added" || stripANSI(plain) != "- item" {
		t.Fatalf("styling changed bytes: %q %q", stripANSI(raw), stripANSI(plain))
	}
	// Sage green for additions, soft red for removals — and only there.
	if !strings.Contains(raw, "125;155;118") {
		t.Errorf("added line missing success color:\n%q", raw)
	}
	if strings.Contains(plain, "255;107;107") {
		t.Errorf("markdown bullet picked up removal color:\n%q", plain)
	}

	e := chatEntry{Role: roleSystem, Content: diff}
	got := stripANSI(e.render(80, false))
	for _, want := range []string{"+added", "-removed", "diff --git"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff body missing %q:\n%s", want, got)
		}
	}
}

func TestSystemHelpStaysExpanded(t *testing.T) {
	// Build the real /help text from the live registry: command reference
	// must never collapse by default.
	reg := newRegistry()
	var b strings.Builder
	b.WriteString("Available commands:\n")
	for _, cmd := range reg.All() {
		fmt.Fprintf(&b, "  %-16s %s\n", cmd.Usage(), cmd.Description())
	}
	content := strings.TrimRight(b.String(), "\n")
	t.Logf("/help is %d rows", len(sysSplitLines(content)))
	if systemStartCollapsed(content) {
		t.Fatalf("/help (%d rows) starts collapsed", len(sysSplitLines(content)))
	}
	e := chatEntry{Role: roleSystem, Content: content}
	got := stripANSI(e.render(80, false))
	if strings.Contains(got, "▸ System") {
		t.Errorf("/help rendered collapsed:\n%s", got)
	}
}

func TestSystemCollapseSurvivesCoalescing(t *testing.T) {
	m := newTestModel()
	for i := 0; i < 40; i++ {
		m.Println("row %d", i)
	}
	if len(m.entries) != 1 {
		t.Fatalf("Println coalescing broke: %d entries", len(m.entries))
	}
	got := stripANSI(m.entries[0].render(m.viewport.Width, false))
	if rows := strings.Split(got, "\n"); len(rows) != 1 {
		t.Fatalf("coalesced 40-line block renders %d rows, want collapsed 1", len(rows))
	}
}

// agentFixture mirrors builtin.Agent.Execute's list shape byte for byte,
// including its ragged first-record indent (outer "  %s" on top of the
// marker's own space).
const agentFixture = "Available agents:\n" +
	"    general — general\n" +
	"    tools: no tools\n" +
	"    skills: no skills\n" +
	"  ● coding — coding\n" +
	"    tools: no tools\n" +
	"    skills: no skills\n" +
	"Active: coding\n" +
	"Usage: /agent <name> to switch (e.g. /agent coding)"

func TestSysGroupParsesAgents(t *testing.T) {
	blocks := parseSysGroup(agentFixture)
	if blocks == nil {
		t.Fatal("agent directory did not group")
	}
	var names []string
	var details int
	for _, b := range blocks {
		if b.section == nil {
			continue
		}
		names = append(names, b.section.name)
		details += len(b.section.details)
	}
	if strings.Join(names, ",") != "general,coding" {
		t.Errorf("sections = %v, want general,coding", names)
	}
	if details != 4 {
		t.Errorf("details = %d, want 4 tools:/skills: rows", details)
	}
}

func TestSysGroupFailsClosed(t *testing.T) {
	for _, content := range []string{
		"Available agents:\n    general — general\nActive: general",
		"Some prose — with an em-dash, but flat.",
		"diff --git a/x b/x\n  ● not-a-record — nope",
		"short",
		"",
	} {
		if blocks := parseSysGroup(content); blocks != nil {
			t.Errorf("grouped %q, want normal path", content)
		}
	}
}

func TestSysGroupRendersCompactRows(t *testing.T) {
	e := chatEntry{Role: roleSystem, Content: agentFixture}
	rows := strings.Split(stripANSI(e.render(100, false)), "\n")
	// Intro + one row per agent + outro; details hidden.
	if len(rows) != 1+2+2 {
		t.Fatalf("collapsed group is %d rows, want 5:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	if !strings.HasPrefix(rows[1], "  ▸ general — general") {
		t.Errorf("row 1 = %q, want aligned compact row", rows[1])
	}
	if !strings.HasPrefix(rows[2], "  ▸ ● coding — coding") {
		t.Errorf("row 2 = %q, want active row with marker", rows[2])
	}
	for _, r := range rows {
		if strings.Contains(r, "tools:") {
			t.Errorf("collapsed group leaks details:\n%s", strings.Join(rows, "\n"))
		}
	}
}

func TestSysGroupActiveReadsBright(t *testing.T) {
	defer forceTrueColor()()
	e := chatEntry{Role: roleSystem, Content: agentFixture}
	got := e.render(100, false)
	if !strings.Contains(got, ansiPink) {
		t.Errorf("active marker missing pink:\n%q", got)
	}
	if !strings.Contains(got, ansiBold) {
		t.Errorf("active name missing bold:\n%q", got)
	}
}

func TestSysGroupExpandsBytesIntact(t *testing.T) {
	e := chatEntry{Role: roleSystem, Content: agentFixture}
	e.SysSections = []bool{true, true}
	rows := strings.Split(stripANSI(e.render(100, false)), "\n")
	var body []string
	for _, r := range rows {
		body = append(body, strings.TrimRight(r, " "))
	}
	// Headers gain carets, so compare detail/outro coverage instead.
	joined := strings.Join(body, "\n")
	for _, want := range []string{"tools: no tools", "skills: no skills", "Active: coding", "Usage: /agent", "general", "coding"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expanded group missing %q:\n%s", want, joined)
		}
	}
}

func TestSysGroupClickOpensOneSection(t *testing.T) {
	m := newTestModel()
	m.entries = []chatEntry{{Role: roleSystem, Content: agentFixture}}
	m.refreshTranscript()
	if len(m.spans) != 2 {
		t.Fatalf("got %d spans, want 1 per agent", len(m.spans))
	}
	second := m.spans[1]
	y := m.headerRows() + second.startLine - m.viewport.YOffset
	next, consumed := m.routeMouse(leftClick(4, y))
	m = next
	if !consumed {
		t.Fatal("section click was not consumed")
	}
	if !sysSectionOpen(m.entries[0], 1) {
		t.Error("click did not open the second section")
	}
	if sysSectionOpen(m.entries[0], 0) {
		t.Error("click leaked into the first section")
	}
}

func TestSysGroupCtrlETogglesAll(t *testing.T) {
	m := newTestModel()
	m.entries = []chatEntry{{Role: roleSystem, Content: agentFixture}}

	m.toggleExpandable()
	if !sysSectionOpen(m.entries[0], 0) || !sysSectionOpen(m.entries[0], 1) {
		t.Fatal("ctrl+e did not expand all sections")
	}
	m.toggleExpandable()
	if sysSectionOpen(m.entries[0], 0) || sysSectionOpen(m.entries[0], 1) {
		t.Fatal("second ctrl+e did not collapse all sections")
	}
}

func TestSysGroupHeaderNeverWraps(t *testing.T) {
	long := "Available agents:\n  ● coding — " + strings.Repeat("verbose description ", 10) + "\n    tools: a\n    skills: b\nActive: coding"
	e := chatEntry{Role: roleSystem, Content: long}
	rows := strings.Split(stripANSI(e.render(40, false)), "\n")
	for i, r := range rows {
		if w := lipgloss.Width(r); w > 40 {
			t.Errorf("row %d overflows (%d > 40): %q", i, w, r)
		}
	}
}

// forceTrueColor pins lipgloss to full ANSI so style assertions hold
// regardless of the CI terminal.
func forceTrueColor() func() {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	return func() { lipgloss.SetColorProfile(prev) }
}

const (
	// Fragments of this lipgloss version's TrueColor output. Bold and
	// underline combine into the opening sequence ("[1;38;2…", "[4;38;2…"),
	// and some hexes round down a step (#7A7A7A renders 121;121;121).
	ansiBold   = "[1;"
	ansiPink   = "237;38;99"   // #ED2663
	ansiGreen  = "125;155;118" // #7D9B76
	ansiYellow = "195;163;89"  // #C4A35A
	ansiRed    = "255;107;107" // #FF6B6B
	ansiMuted  = "121;121;121" // #7A7A7A
	ansiUnder  = "[4;"
)

func TestSystemInlinePreservesBytes(t *testing.T) {
	lines := []string{
		"See https://example.com/x.",
		"Add files to ~/.forcefield/skills/review.md today.",
		`Open C:\repo\x.txt for details.`,
		"Released v1.5.1 alongside v1.5.0.",
		"e.g. docs and i.e. examples",
		"Use and/or logic in utf-8 mode on srv2.",
		"Set read-only and no-follow flags.",
		"Meet at 12:30 or 2026-10-06T10:48:03Z.",
		"Run 1,000 times at 100% by step 3.",
		"Use `inline code` and /build to proceed.",
		"Unsandboxed MCP servers run with OS privileges.",
		"",
	}
	for _, line := range lines {
		// Both tiers (plain line vs. one-liner entry) preserve bytes.
		for _, single := range []bool{false, true} {
			if got := stripANSI(styleSystemLine(line, single, false)); got != line {
				t.Errorf("single=%v line %q became %q", single, line, got)
			}
		}
	}
}

func TestSystemOneLinerStatus(t *testing.T) {
	defer forceTrueColor()()
	for _, tc := range []struct {
		line string
		want string
	}{
		{"Build complete.", ansiGreen},
		{"No background jobs.", ansiMuted},
		{"Run cancelled.", ansiMuted},
		{"Save failed: disk full", ansiRed},
	} {
		got := chatEntry{Role: roleSystem, Content: tc.line}.render(80, false)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%q missing status color:\n%q", tc.line, got)
		}
		if stripped := stripANSI(got); !strings.Contains(stripped, tc.line) {
			t.Errorf("%q bytes changed:\n%q", tc.line, stripped)
		}
	}
}

func TestSystemCheckConfirmation(t *testing.T) {
	defer forceTrueColor()()
	got := chatEntry{Role: roleSystem, Content: "✓ Provider: ollama"}.render(80, false)
	for _, want := range []string{ansiGreen, ansiMuted, "Provider:", "ollama"} {
		if !strings.Contains(got, want) {
			t.Errorf("confirmation missing %q:\n%q", want, got)
		}
	}
}

func TestSystemHeadingsBulletsTablesSeps(t *testing.T) {
	defer forceTrueColor()()
	content := "MCP SERVERS\nAvailable tools:\n  - shell\n  1. first\n| name | state |\n| ---- | ----- |\n| demo | ready |\n---\nplain tail"
	e := chatEntry{Role: roleSystem, Content: content}
	rows := strings.Split(stripANSI(e.render(80, false)), "\n")
	body := make([]string, 0, len(rows))
	for _, r := range rows[1:] {
		body = append(body, strings.TrimRight(r, " "))
	}
	rendered := e.render(80, false)
	if got := strings.Join(body, "\n"); got != content {
		t.Errorf("bytes changed:\n got %q\nwant %q", got, content)
	}
	for _, want := range []string{ansiBold, ansiMuted} {
		if !strings.Contains(rendered, want) {
			t.Errorf("hierarchy missing %q:\n%q", want, rendered)
		}
	}
}

func TestSystemTiers(t *testing.T) {
	defer forceTrueColor()()
	prose := "Check error handling in `review.md` under ~/skills and complete setup on demand."
	multi := chatEntry{Role: roleSystem, Content: prose + "\nsecond line here"}.render(80, false)
	for _, bad := range []string{ansiRed, ansiGreen, ansiPink, ansiYellow} {
		if strings.Contains(multi, bad) {
			t.Errorf("prose picked up word color %q:\n%q", bad, multi)
		}
	}
	if !strings.Contains(multi, ansiBold) || !strings.Contains(multi, ansiUnder) {
		t.Errorf("prose lost reference styling:\n%q", multi)
	}
	single := chatEntry{Role: roleSystem, Content: prose}.render(80, false)
	for _, want := range []string{ansiRed, ansiGreen} {
		if !strings.Contains(single, want) {
			t.Errorf("one-liner missing status color %q:\n%q", want, single)
		}
	}
}

func TestSystemGluedWordsStayPlain(t *testing.T) {
	defer forceTrueColor()()
	for _, line := range []string{
		"read-only mode",
		"and/or logic",
		"e.g. docs",
		"utf-8 text on srv2",
	} {
		// Multi-line tier: glued fragments must not pick up word colors.
		got := styleSystemLine(line, false, false)
		for _, bad := range []string{ansiGreen, ansiRed, ansiYellow, ansiPink, ansiUnder} {
			if strings.Contains(got, bad) {
				t.Errorf("%q gained styling %q:\n%q", line, bad, got)
			}
		}
	}
}

func TestSystemNumbersTimestamps(t *testing.T) {
	defer forceTrueColor()()
	got := chatEntry{Role: roleSystem, Content: "Messages:  3 (~100 B) at 2026-10-06T10:48:03Z"}.render(80, false)
	for _, want := range []string{ansiBold, ansiMuted} {
		if !strings.Contains(got, want) {
			t.Errorf("numbers/timestamps missing styling %q:\n%q", want, got)
		}
	}
	if stripped := stripANSI(got); !strings.Contains(stripped, "Messages:  3 (~100 B) at 2026-10-06T10:48:03Z") {
		t.Errorf("bytes changed:\n%q", stripped)
	}
}
