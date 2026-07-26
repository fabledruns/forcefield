package shell

import (
	"context"
	"os"
	"testing"
)

func TestPWD_ReturnsWorkingDirectory(t *testing.T) {
	want, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() unexpected error: %v", err)
	}

	p := NewPWD()
	res, err := p.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute() result.IsError = true, content: %s", res.Content)
	}
	if res.Content != want {
		t.Fatalf("Execute() content = %q, want %q", res.Content, want)
	}
}
