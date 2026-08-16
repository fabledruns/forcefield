package runtime

import (
	"strings"
	"testing"

	"forcefield/internal/agent"
)

func TestBuildMessagesIncludesProjectMemory(t *testing.T) {
	a := agent.New("test", "system prompt", "").
		WithProjectMemory("- uses go 1.24\n- run tests with make test")

	rt := &Runtime{agent: a}

	messages := rt.buildMessages(nil)
	if len(messages) == 0 {
		t.Fatalf("expected at least the system message")
	}

	system := messages[0].Content
	if !strings.Contains(system, "Project Memory") {
		t.Fatalf("expected the system prompt to include a Project Memory section, got:\n%s", system)
	}
	if !strings.Contains(system, "uses go 1.24") {
		t.Fatalf("expected remembered facts to appear in the system prompt, got:\n%s", system)
	}
}

func TestBuildMessagesOmitsProjectMemorySectionWhenEmpty(t *testing.T) {
	a := agent.New("test", "system prompt", "")
	rt := &Runtime{agent: a}

	messages := rt.buildMessages(nil)
	system := messages[0].Content
	if strings.Contains(system, "Project Memory") {
		t.Fatalf("expected no Project Memory section when nothing has been remembered, got:\n%s", system)
	}
}
