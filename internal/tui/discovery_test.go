package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/config"
	"forcefield/internal/session"
)

// discoveryChatModel builds a fully wired chat model whose "lab" provider
// is an OpenAI-compatible endpoint backed by the given handler, plus a
// channel capturing every notify message the TUI emits and the request
// counter.
func discoveryChatModel(t *testing.T, handler http.HandlerFunc) (model, <-chan tea.Msg, *atomic.Int64) {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	var count atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".forcefield", "config.yaml")
	body := fmt.Sprintf(`model:
  provider: lab
  name: custom-model

providers:
  lab:
    type: openai-compatible
    base_url: %s
`, server.URL)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}

	m, err := newModel(cfg, session.New(), &tuiAsker{})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.width, m.height = 100, 30
	m.layout()

	msgs := make(chan tea.Msg, 16)
	m.notify = func(msg tea.Msg) {
		select {
		case msgs <- msg:
		default:
		}
	}
	return m, msgs, &count
}

func waitMsg(t *testing.T, msgs <-chan tea.Msg) tea.Msg {
	t.Helper()
	select {
	case msg := <-msgs:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("no message arrived within 5s")
		return nil
	}
}

func pickerModelIDs(picker *selectPicker) []string {
	var ids []string
	for _, opt := range picker.options {
		if opt.ID != refreshOptionID {
			ids = append(ids, opt.ID)
		}
	}
	return ids
}

func TestModelPickerFetchesWhileShowingFallbacks(t *testing.T) {
	m, msgs, _ := discoveryChatModel(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"zulu"},{"id":"alpha"}]}`)
	})

	m.OpenModelPicker()
	picker := m.selectPicker
	if picker == nil || picker.scope != scopeModel || picker.provider != "lab" {
		t.Fatal("model picker did not open for lab")
	}
	if !picker.fetching {
		t.Fatal("picker did not enter the fetching state for a stale listing")
	}
	if got := picker.options[0].ID; got != "custom-model" {
		t.Errorf("first option = %q, want the explicitly configured model", got)
	}
	if box := picker.box(); !strings.Contains(box, "Fetching models") {
		t.Errorf("picker does not show a loading state:\n%s", box)
	}

	// The background fetch reports back through notify; feed it through
	// Update exactly as bubbletea would.
	fetched, ok := waitMsg(t, msgs).(modelsFetchedMsg)
	if !ok || fetched.provider != "lab" {
		t.Fatalf("msg = %#v, want modelsFetchedMsg for lab", waitMsgResult)
	}

	next, _ := m.Update(fetched)
	got := next.(model)
	picker = got.selectPicker
	if picker == nil {
		t.Fatal("picker closed when results arrived")
	}
	if picker.fetching {
		t.Error("picker stuck in fetching state after results")
	}
	if picker.status != "" {
		t.Errorf("unexpected status %q after success", picker.status)
	}
	if ids := fmt.Sprint(pickerModelIDs(picker)); ids != "[custom-model alpha zulu]" {
		t.Errorf("ids = %s, want active model first then discovered sorted", ids)
	}
}

// waitMsgResult exists only so the type assertion in a fatal path can
// print something meaningful without shadowing errors.
var waitMsgResult tea.Msg

func TestModelPickerFailureKeepsModelsAndShowsStatus(t *testing.T) {
	m, msgs, _ := discoveryChatModel(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"exploded"}}`)
	})

	m.OpenModelPicker()
	before := pickerModelIDs(m.selectPicker)
	if len(before) == 0 {
		t.Fatalf("no fallback rows before fetch: %+v", m.selectPicker.options)
	}

	fetched, ok := waitMsg(t, msgs).(modelsFetchedMsg)
	if !ok {
		t.Fatalf("unexpected message while waiting for discovery result")
	}
	next, _ := m.Update(fetched)
	got := next.(model)
	picker := got.selectPicker

	if picker.fetching {
		t.Error("picker stuck fetching after failure")
	}
	if !strings.Contains(picker.status, "⚠") {
		t.Errorf("status = %q, want a concise warning line", picker.status)
	}

	after := pickerModelIDs(picker)
	for _, want := range before {
		found := false
		for _, id := range after {
			if id == want {
				found = true
			}
		}
		if !found {
			t.Errorf("option %q disappeared after failed discovery", want)
		}
	}
}

func TestRefreshActionBypassesCacheAndPreservesSelection(t *testing.T) {
	m, msgs, count := discoveryChatModel(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"alpha"},{"id":"beta"}]}`)
	})

	// First listing arrives via the automatic stale-fetch.
	m.OpenModelPicker()
	fetched, ok := waitMsg(t, msgs).(modelsFetchedMsg)
	if !ok {
		t.Fatal("unexpected message while waiting for initial discovery")
	}
	updated, _ := m.Update(fetched)
	m = updated.(model)
	if n := count.Load(); n != 1 {
		t.Fatalf("requests = %d, want exactly the initial fetch", n)
	}

	// Park the cursor on the configured model so we can prove a refresh
	// preserves the selection.
	for i, opt := range m.selectPicker.options {
		if opt.ID == "custom-model" {
			m.selectPicker.cursor = i
		}
	}

	next := m
	next.triggerModelRefresh()
	if next.selectPicker == nil || !next.selectPicker.fetching {
		t.Fatal("refresh did not re-enter the fetching state")
	}

	msg := waitMsg(t, msgs)
	fetched2, ok2 := msg.(modelsFetchedMsg)
	if !ok2 {
		t.Fatal("unexpected message while waiting for refresh result")
	}
	updated2, _ := next.Update(fetched2)
	got := updated2.(model)

	if got.selectPicker.fetching {
		t.Error("picker stuck fetching after refresh completed")
	}
	if got.selectPicker.options[got.selectPicker.cursor].ID != "custom-model" {
		t.Errorf("cursor landed on %q, want the previously selected model",
			got.selectPicker.options[got.selectPicker.cursor].ID)
	}
	if n := count.Load(); n != 2 {
		t.Fatalf("requests = %d, refresh must bypass the cache", n)
	}
}

func TestLateDiscoveryResultForOtherProviderIsDropped(t *testing.T) {
	m, _, _ := discoveryChatModel(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"alpha"}]}`)
	})

	m.OpenModelPicker()
	rowsBefore := len(m.selectPicker.options)

	next, _ := m.Update(modelsFetchedMsg{provider: "somebody-else"})
	got := next.(model)

	if got.selectPicker == nil || len(got.selectPicker.options) != rowsBefore {
		t.Fatal("a foreign provider's late result mutated the open picker")
	}
	if !got.selectPicker.fetching {
		t.Error("own in-flight fetch was disturbed by the foreign result")
	}
}
