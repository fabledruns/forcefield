package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

var errSecretBoom = errors.New("upstream exploded on sk-12345678901234567890abcdef")

// secretBodyTool returns a fixed body standing in for a large skill
// body embedding a secret.

// secretStreamingTool emits a live chunk and a final result that both
// carry secrets, proving both the progress path and the result path
// scrub before anything reaches the transcript or session.
type secretStreamingTool struct{}

func (secretStreamingTool) Name() string        { return "secretstream" }
func (secretStreamingTool) Description() string { return "streams secrets" }
func (secretStreamingTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (secretStreamingTool) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	return secretStreamingTool{}.ExecuteStream(ctx, args, nil)
}
func (secretStreamingTool) ExecuteStream(_ context.Context, _ map[string]any, onChunk func(tools.StreamChunk)) (tools.Result, error) {
	if onChunk != nil {
		onChunk(tools.StreamChunk{Stream: "stdout", Data: "leaking sk-12345678901234567890abcdef live"})
	}
	return tools.Result{Content: "done sk-12345678901234567890abcdef"}, nil
}

func TestScheduler_ScrubsProgressAndResult(t *testing.T) {
	manager := newTestManager(t, &secretStreamingTool{})
	s := newScheduler(manager, nil, nil, DefaultSchedulerConfig)

	var chunks []string
	var final *ToolResult
	emit := func(e Event) bool {
		switch e.Type {
		case EventToolProgress:
			chunks = append(chunks, e.ToolProgress.Data)
		case EventToolFinish:
			final = e.ToolResult
		}
		return true
	}
	s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "secretstream"}}, emit)

	const secret = "sk-12345678901234567890abcdef"
	for _, c := range chunks {
		if strings.Contains(c, secret) {
			t.Errorf("progress chunk leaked secret: %q", c)
		}
	}
	if len(chunks) != 1 {
		t.Fatalf("chunks = %v, want 1 scrubbed chunk", chunks)
	}
	if final == nil {
		t.Fatal("no ToolFinish event")
	}
	if strings.Contains(final.Content, secret) {
		t.Errorf("result leaked secret: %q", final.Content)
	}
	if !strings.Contains(final.Content, "[redacted]") {
		t.Errorf("result lacks redaction marker: %q", final.Content)
	}
}

// secretBodyTool returns a large body embedding a secret, standing in
// for a 1 MiB skill body: the scheduler must scrub before the content
// fans out to session, transcript, and provider replay.
type secretBodyTool struct{ body string }

func (t *secretBodyTool) Name() string        { return "load_skill" }
func (t *secretBodyTool) Description() string { return "body" }
func (t *secretBodyTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (t *secretBodyTool) Execute(context.Context, map[string]any) (tools.Result, error) {
	return tools.Result{Content: t.body}, nil
}

func TestScheduler_ScrubsLargeSecretBearingBody(t *testing.T) {
	body := strings.Repeat("guidance line\n", 500) + "password = \"hunter2-hunter2\"\n"
	manager := newTestManager(t, &secretBodyTool{body: body})
	s := newScheduler(manager, nil, nil, DefaultSchedulerConfig)

	var final *ToolResult
	s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "load_skill"}},
		func(e Event) bool {
			if e.Type == EventToolFinish {
				final = e.ToolResult
			}
			return true
		})

	if final == nil {
		t.Fatal("no ToolFinish event")
	}
	if strings.Contains(final.Content, "hunter2-hunter2") {
		t.Errorf("body secret survived scheduler scrub (%.200s...)", final.Content)
	}
}

// TestRun_StreamErrorSecretsScrubbed pins that a provider failure
// echoing credentials surfaces cleanly while staying matchable.
func TestRun_StreamErrorSecretsScrubbed(t *testing.T) {
	p := &attemptProvider{outcomes: []attemptOutcome{
		{events: []providers.StreamEvent{
			{Err: errSecretBoom},
		}},
	}}
	rt := newTestRuntime(p)

	_, err := rt.RunContext(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err == nil {
		t.Fatal("expected the stream failure")
	}
	if strings.Contains(err.Error(), "sk-12345678901234567890abcdef") {
		t.Errorf("run error leaked secret: %q", err)
	}
}
