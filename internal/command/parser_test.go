package command

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   ParsedInput
		wantOK bool
	}{
		{
			name:   "simple command",
			line:   "/help",
			want:   ParsedInput{Name: "help", Args: []string{}},
			wantOK: true,
		},
		{
			name:   "command with one argument",
			line:   "/model qwen3:8b",
			want:   ParsedInput{Name: "model", Args: []string{"qwen3:8b"}},
			wantOK: true,
		},
		{
			name:   "command with multiple arguments and extra spacing",
			line:   "  /provider   ollama   local  ",
			want:   ParsedInput{Name: "provider", Args: []string{"ollama", "local"}},
			wantOK: true,
		},
		{
			name:   "is lowercased",
			line:   "/HELP",
			want:   ParsedInput{Name: "help", Args: []string{}},
			wantOK: true,
		},
		{
			name:   "plain chat message is not a command",
			line:   "what's the weather like?",
			want:   ParsedInput{},
			wantOK: false,
		},
		{
			name:   "bare slash is not a command",
			line:   "/",
			want:   ParsedInput{},
			wantOK: false,
		},
		{
			name:   "slash with only whitespace is not a command",
			line:   "/   ",
			want:   ParsedInput{},
			wantOK: false,
		},
		{
			name:   "empty line is not a command",
			line:   "",
			want:   ParsedInput{},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Parse(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("Parse(%q) ok = %v, want %v", tt.line, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got.Name != tt.want.Name || !reflect.DeepEqual(got.Args, tt.want.Args) {
				t.Fatalf("Parse(%q) = %+v, want %+v", tt.line, got, tt.want)
			}
		})
	}
}
