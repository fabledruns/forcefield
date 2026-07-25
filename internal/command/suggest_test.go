package command

import "testing"

func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"help", "help", 0},
		{"", "help", 4},
		{"help", "", 4},
		{"hlep", "help", 2},
		{"exit", "exti", 2},
		{"clear", "clear", 0},
		{"kitten", "sitting", 3}, 
	}

	for _, tt := range tests {
		if got := levenshtein(tt.a, tt.b); got != tt.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
