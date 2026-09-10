package permissions

import (
	"sync"
	"testing"
)

type memStore struct {
	mu    sync.Mutex
	rules Rules
	saves int
}

func (s *memStore) Load() (Rules, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rules.clone(), nil
}

func (s *memStore) Save(r Rules) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = r.clone()
	s.saves++
	return nil
}

func TestCheckFallsBackToDefault(t *testing.T) {
	store := &memStore{rules: Rules{Default: Ask, Tools: map[string]Decision{"read_file": Allow}}}
	m, err := NewManager(store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	if got := m.Check("read_file"); got != Allow {
		t.Errorf("Check(read_file) = %v, want Allow", got)
	}
	if got := m.Check("shell"); got != Ask {
		t.Errorf("Check(shell) = %v, want Ask (default)", got)
	}
}

// TestRuntimeToolsDefaultToAsk pins the N16 contract at the enforcement
// layer: under shipped-default rules (default ask, no per-tool entry),
// load_skill and update_task_state must require approval, never
// auto-allow. An explicit operator override still wins.
func TestRuntimeToolsDefaultToAsk(t *testing.T) {
	store := &memStore{rules: Rules{Default: Ask, Tools: map[string]Decision{}}}
	m, err := NewManager(store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	for _, tool := range []string{"load_skill", "update_task_state"} {
		if got := m.Check(tool); got != Ask {
			t.Errorf("Check(%s) = %v, want Ask (fail-closed default)", tool, got)
		}
	}

	if err := m.Update("load_skill", Allow); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := m.Check("load_skill"); got != Allow {
		t.Errorf("Check(load_skill) after explicit allow = %v, want Allow", got)
	}
}

func TestUpdatePersists(t *testing.T) {
	store := &memStore{rules: Rules{Default: Ask, Tools: map[string]Decision{}}}
	m, err := NewManager(store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	if err := m.Update("shell", Deny); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := m.Check("shell"); got != Deny {
		t.Errorf("Check(shell) after Update = %v, want Deny", got)
	}
	if store.saves != 1 {
		t.Errorf("store.saves = %d, want 1", store.saves)
	}

	m2, err := NewManager(store)
	if err != nil {
		t.Fatalf("NewManager (reload): %v", err)
	}
	if got := m2.Check("shell"); got != Deny {
		t.Errorf("reloaded Check(shell) = %v, want Deny", got)
	}
}

func TestConcurrentCheckAndUpdate(t *testing.T) {
	store := &memStore{rules: Rules{Default: Allow, Tools: map[string]Decision{}}}
	m, err := NewManager(store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			m.Check("shell")
		}()
		go func() {
			defer wg.Done()
			_ = m.Update("shell", Deny)
		}()
	}
	wg.Wait() // must not race or deadlock (run with -race)
}

func TestParseDecision(t *testing.T) {
	cases := map[string]Decision{
		"allow": Allow,
		"deny":  Deny,
		"ask":   Ask,
		"":      Ask,
	}
	for in, want := range cases {
		got, err := ParseDecision(in)
		if err != nil {
			t.Errorf("ParseDecision(%q) unexpected error: %v", in, err)
		}
		if got != want {
			t.Errorf("ParseDecision(%q) = %v, want %v", in, got, want)
		}
	}

	if _, err := ParseDecision("maybe"); err == nil {
		t.Error("ParseDecision(\"maybe\") expected error, got nil")
	}
}

func TestPromptSemantics(t *testing.T) {
	cases := []struct {
		p        Prompt
		decision Decision
		persist  bool
	}{
		{PromptAllowOnce, Allow, false},
		{PromptDenyOnce, Deny, false},
		{PromptAlwaysAllow, Allow, true},
		{PromptAlwaysDeny, Deny, true},
	}
	for _, c := range cases {
		if got := c.p.Decision(); got != c.decision {
			t.Errorf("%v.Decision() = %v, want %v", c.p, got, c.decision)
		}
		if got := c.p.Persist(); got != c.persist {
			t.Errorf("%v.Persist() = %v, want %v", c.p, got, c.persist)
		}
	}
}
