package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/agent"
	"forcefield/internal/providers"
	"forcefield/internal/skills"
)

// fixtureSkillStore builds a real skill store in a temp home with three
// skills: alpha, beta, intelligence.
func fixtureSkillStore(t *testing.T) *skills.Store {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("alpha.md", "---\nid: alpha\nname: Alpha\ndescription: first fixture skill\n---\n\nAlpha body.\n")
	write("beta.md", "---\nid: beta\nname: Beta\ndescription: second fixture skill\n---\n\nBeta body.\n")
	write("intelligence.md", "# Intelligence\n\nReasoning framework fixture.\n")
	store, err := skills.New(home)
	if err != nil {
		t.Fatalf("skills.New: %v", err)
	}
	if len(store.Catalog()) != 3 {
		t.Fatalf("want 3 fixture skills, got %d", len(store.Catalog()))
	}
	return store
}

func toolNameSet(rt *Runtime) map[string]bool {
	set := make(map[string]bool)
	for _, d := range rt.manager.Definitions() {
		set[d.Name] = true
	}
	return set
}

func expectTools(t *testing.T, rt *Runtime, want ...string) {
	t.Helper()
	got := toolNameSet(rt)
	if len(got) != len(want) {
		t.Fatalf("tool count = %d (%v), want %d (%v)", len(got), keys(got), len(want), want)
	}
	for _, w := range want {
		if !got[w] {
			t.Fatalf("missing tool %q in %v", w, keys(got))
		}
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

var (
	fullSet  = []string{"read_file", "write_file", "list_files", "pwd", "shell", "search_files", "secret_scan", "load_skill", "update_task_state", "add_project_memory"}
	cyberSet = []string{"read_file", "list_files", "pwd", "shell", "search_files", "secret_scan", "load_skill", "update_task_state", "add_project_memory"}
	legalSet = []string{"read_file", "list_files", "pwd", "search_files", "load_skill", "update_task_state", "add_project_memory"}
)

// TestCapabilityTransition is the end-to-end capability test:
// general → cyber → coding → legal (and back), asserting the exact tool
// set, skill catalog, and fail-closed rejection at every step.
func TestCapabilityTransition(t *testing.T) {
	rt := newAgentTestRuntime(t, &scriptedProvider{turns: [][]providers.StreamEvent{{{Done: true}}}})
	rt.skills = fixtureSkillStore(t)
	// Rebuild the initial agent now that a store exists (construction
	// built it with a nil store, same as a fresh home with no skills).
	def, _ := rt.agents.Get(rt.activeAgent)
	rt.agent = rt.buildAgent(def)

	catalogIDs := func() []string {
		prompt := rt.agent.BuildSystemPrompt()
		var ids []string
		for _, id := range []string{"alpha", "beta", "intelligence", "code-review", "debugging"} {
			if strings.Contains(prompt, "`"+id+"`") {
				ids = append(ids, id)
			}
		}
		return ids
	}

	// general: all tools, all (3 fixture) skills.
	if got := rt.CurrentAgent(); got != "general" {
		t.Fatalf("start agent = %q", got)
	}
	expectTools(t, rt, fullSet...)
	if ids := catalogIDs(); len(ids) != 3 {
		t.Fatalf("general catalog = %v, want all 3 fixtures", ids)
	}

	// cyber: 9 tools; catalog = intelligence only (code-review missing).
	if err := rt.SetAgent("cyber"); err != nil {
		t.Fatalf("SetAgent cyber: %v", err)
	}
	expectTools(t, rt, cyberSet...)
	if ids := catalogIDs(); len(ids) != 1 || ids[0] != "intelligence" {
		t.Fatalf("cyber catalog = %v, want [intelligence]", ids)
	}
	assertToolRejected(t, rt, "write_file")
	assertSkillRefused(t, rt, "alpha") // exists, unassigned to cyber

	// coding: full 10 tools; catalog = intelligence only (rest missing).
	if err := rt.SetAgent("coding"); err != nil {
		t.Fatalf("SetAgent coding: %v", err)
	}
	expectTools(t, rt, fullSet...)
	if ids := catalogIDs(); len(ids) != 1 || ids[0] != "intelligence" {
		t.Fatalf("coding catalog = %v, want [intelligence]", ids)
	}
	assertSkillRefused(t, rt, "beta")

	// legal: 7 tools; empty catalog (no assignment).
	if err := rt.SetAgent("legal"); err != nil {
		t.Fatalf("SetAgent legal: %v", err)
	}
	expectTools(t, rt, legalSet...)
	if ids := catalogIDs(); len(ids) != 0 {
		t.Fatalf("legal catalog = %v, want empty", ids)
	}
	assertToolRejected(t, rt, "shell")
	assertToolRejected(t, rt, "secret_scan")
	assertSkillRefused(t, rt, "intelligence")  // exists, unassigned to legal
	assertSkillMissing(t, rt, "no-such-skill") // absent entirely

	// Switch back to general restores the full profile.
	if err := rt.SetAgent("general"); err != nil {
		t.Fatalf("SetAgent general: %v", err)
	}
	expectTools(t, rt, fullSet...)
	if ids := catalogIDs(); len(ids) != 3 {
		t.Fatalf("restored general catalog = %v, want all 3", ids)
	}
	assertSkillLoadable(t, rt, "alpha", "Alpha body.")
}

// assertToolRejected runs one model turn requesting a tool outside the
// active agent and requires fail-closed "tool not found". It drains the
// event channel through its terminal event: returning early on
// EventToolFailed would leave the background run writing to r.agent while
// the caller switches agents, which races under -race and leaks a goroutine
// blocked on an unconsumed channel.
func assertToolRejected(t *testing.T, rt *Runtime, tool string) {
	t.Helper()
	rt.provider = &scriptedProvider{turns: [][]providers.StreamEvent{
		{{ToolCalls: []providers.ToolCall{{ID: "c1", Name: tool, Arguments: map[string]any{}}}}, {Done: true}},
		{{Text: "done"}, {Done: true}},
	}}
	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "x"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	rejected := false
	for ev := range events {
		if ev.Type == EventToolFailed && ev.ToolResult != nil && ev.ToolResult.Name == tool {
			if !strings.Contains(ev.ToolResult.Content, "tool not found") {
				t.Fatalf("tool %q failed without fail-closed message: %q", tool, ev.ToolResult.Content)
			}
			rejected = true
			continue
		}
		if ev.Type == EventError {
			t.Fatalf("unexpected run error: %v", ev.Err)
		}
		if ev.Type == EventDone || ev.Type == EventBlocked {
			break
		}
	}
	if !rejected {
		t.Fatalf("tool %q was not rejected", tool)
	}
}

// assertSkillRefused requires load_skill to refuse an existing-but-unassigned skill.
// Test runtimes register echo stand-ins for tool names, so the real scoped
// tool is constructed directly with the live allow-set closures.
func assertSkillRefused(t *testing.T, rt *Runtime, id string) {
	t.Helper()
	ls := newLoadSkillTool(rt.skills)
	ls.allowed = rt.agentSkillSet
	ls.agentName = rt.CurrentAgent
	res, err := ls.Execute(context.Background(), map[string]any{"id": id})
	if err != nil {
		t.Fatalf("load_skill must fail soft, got hard: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "not available to") {
		t.Fatalf("want not-available refusal for %q, got: %q", id, res.Content)
	}
	if strings.Contains(res.Content, "Alpha body.") || strings.Contains(res.Content, "Beta body.") {
		t.Fatalf("refusal must never leak a body, got: %q", res.Content)
	}
}

// assertSkillMissing requires the absent-skill message (distinct from refusal).
func assertSkillMissing(t *testing.T, rt *Runtime, id string) {
	t.Helper()
	ls := newLoadSkillTool(rt.skills)
	ls.allowed = rt.agentSkillSet
	ls.agentName = rt.CurrentAgent
	res, err := ls.Execute(context.Background(), map[string]any{"id": id})
	if err != nil {
		t.Fatalf("load_skill must fail soft, got hard: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "not found in the catalog") {
		t.Fatalf("want not-found message for %q, got: %q", id, res.Content)
	}
}

// assertSkillLoadable requires an assigned skill to load its real body.
func assertSkillLoadable(t *testing.T, rt *Runtime, id, wantBody string) {
	t.Helper()
	ls := newLoadSkillTool(rt.skills)
	ls.allowed = rt.agentSkillSet
	ls.agentName = rt.CurrentAgent
	res, err := ls.Execute(context.Background(), map[string]any{"id": id})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError || !strings.Contains(res.Content, wantBody) {
		t.Fatalf("want body %q, got: %q", wantBody, res.Content)
	}
}

func TestSkillWarningsListMissingAssignments(t *testing.T) {
	rt := newAgentTestRuntime(t, &scriptedProvider{turns: [][]providers.StreamEvent{{{Done: true}}}})
	rt.skills = fixtureSkillStore(t)
	warns := rt.SkillWarnings()
	if len(warns) == 0 {
		t.Fatalf("expected warnings for uninstalled example skills")
	}
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, "code-review") {
		t.Fatalf("warnings should name code-review, got:\n%s", joined)
	}
	// intelligence exists → no warning for it.
	for _, w := range warns {
		if strings.Contains(w, `"intelligence"`) {
			t.Fatalf("no warning expected for installed intelligence: %q", w)
		}
	}
}

func TestFailedAgentSwitchKeepsCapabilities(t *testing.T) {
	rt := newAgentTestRuntime(t, &scriptedProvider{turns: [][]providers.StreamEvent{{{Done: true}}}})
	rt.skills = fixtureSkillStore(t)
	if err := rt.SetAgent("coding"); err != nil {
		t.Fatalf("SetAgent coding: %v", err)
	}
	beforeTools := toolNameSet(rt)
	beforePrompt := rt.agent.BuildSystemPrompt()
	beforeSet := rt.agentSkillSet()

	badProv := agent.Definition{Name: "badprov2", Description: "bad", SystemPrompt: "p", Tools: []string{"read_file"}, Skills: []string{}, Provider: "unknown-provider-xyz"}
	if err := rt.agents.Register(badProv); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := rt.SetAgent("badprov2"); err == nil {
		t.Fatalf("expected switch failure")
	}
	if got := rt.CurrentAgent(); got != "coding" {
		t.Fatalf("agent = %q after failed switch", got)
	}
	afterTools := toolNameSet(rt)
	if len(afterTools) != len(beforeTools) {
		t.Fatalf("tool set changed across failed switch: %v -> %v", keys(beforeTools), keys(afterTools))
	}
	if rt.agent.BuildSystemPrompt() != beforePrompt {
		t.Fatalf("prompt changed across failed switch")
	}
	afterSet := rt.agentSkillSet()
	if len(afterSet) != len(beforeSet) {
		t.Fatalf("skill set changed across failed switch")
	}
}

func TestConstraintsRenderAsBoundaries(t *testing.T) {
	rt := newAgentTestRuntime(t, &scriptedProvider{turns: [][]providers.StreamEvent{{{Done: true}}}})
	rt.skills = fixtureSkillStore(t)
	if err := rt.SetAgent("cyber"); err != nil {
		t.Fatalf("SetAgent cyber: %v", err)
	}
	prompt := rt.agent.BuildSystemPrompt()
	if !strings.Contains(prompt, "## Boundaries") {
		t.Fatalf("cyber prompt must render Boundaries section")
	}
	if !strings.Contains(prompt, "exploit code") {
		t.Fatalf("cyber constraint missing from prompt")
	}
	if err := rt.SetAgent("general"); err != nil {
		t.Fatalf("SetAgent general: %v", err)
	}
	if strings.Contains(rt.agent.BuildSystemPrompt(), "## Boundaries") {
		t.Fatalf("general has no constraints; section must be absent")
	}
}
