package tui

import (
	"context"
	"fmt"
	"strings"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"
)

// modelsFetchedMsg reports the outcome of one background model-discovery
// request. It is delivered through model.notify (program.Send) rather
// than a tea.Cmd so both the /model command path and the provider-picker
// path can trigger discovery uniformly; Update drops it unless the model
// picker is still open for the same provider.
type modelsFetchedMsg struct {
	provider string
	models   []providers.Model
	err      error
}

// refreshOptionID marks the "Refresh models" row inside the model picker.
const refreshOptionID = "__refresh_models__"

// startDiscovery kicks off one background discovery for providerID and
// reports the result via m.notify. The request itself is bounded by the
// runtime; it never blocks the UI and never touches the transcript:
// failures surface in the picker's status line while the previously
// visible models stay listed. With force unset, an already-fresh cache
// skips the network entirely.
func (m *model) startDiscovery(providerID string, force bool) {
	if m.runtime == nil || m.notify == nil {
		return
	}
	if !force {
		if _, state := m.runtime.ModelCatalog(providerID); state == runtime.ModelsFresh {
			return // cache still valid; nothing to fetch
		}
	}

	runtimeRef := m.runtime
	notify := m.notify
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		models, err := runtimeRef.DiscoverModels(ctx, providerID)
		notify(modelsFetchedMsg{provider: providerID, models: models, err: err})
	}()
}

// applyDiscoveredModels rebuilds the open model picker's rows after a
// discovery attempt, preserving the cursor position when the previously
// highlighted model still exists. On failure the previous rows are kept
// and a concise status line explains what happened - discovery failure
// never removes usable choices.
func (m *model) applyDiscoveredModels(msg modelsFetchedMsg) {
	picker := m.selectPicker
	if picker == nil || picker.scope != scopeModel || picker.provider != msg.provider {
		return // picker closed or switched away; drop the stale result
	}

	previous := ""
	if len(picker.options) > 0 && picker.cursor < len(picker.options) {
		previous = picker.options[picker.cursor].ID
	}

	models, state := m.runtime.ModelCatalog(msg.provider)
	picker.options = modelOptions(models, "", state)
	picker.heights = nil
	picker.offset = 0
	picker.cursor = 0
	for i, opt := range picker.options {
		if opt.ID == previous && opt.ID != refreshOptionID {
			picker.cursor = i
			break
		}
	}

	switch {
	case msg.err != nil:
		picker.status = compactError(msg.err)
	default:
		picker.status = ""
	}
	picker.fetching = false
	m.layout()
}

// compactError flattens an error into the one-line, length-capped form
// shown in the picker's status line. Error bodies may embed server text;
// they have already passed credential redaction upstream.
func compactError(err error) string {
	text := strings.ReplaceAll(err.Error(), "\n", " · ")
	text = strings.TrimSpace(text)
	const maxStatusChars = 120
	if len(text) > maxStatusChars {
		text = text[:maxStatusChars-1] + "…"
	}
	return fmt.Sprintf("⚠ %s", text)
}
