package shell

import "testing"

func TestDetectInteractive_BashCBypass(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"bash -c \"vim\"", true},
		{"bash -c 'vim'", true},
		{"sh -c \"ssh host\"", true},
		{"bash -lc \"top\"", true},
		{"zsh -c 'nano file'", true},
		{"bash -c \"echo hi\"", false},
		{"bash -c \"ls -la\"", false},
		{"sh -c \"python\"", true}, // bare repl
		{"sh -c \"python3 -c 'print(1)'\"", false},
		{"sudo bash -c \"vim\"", true},
		{"echo hi && bash -c \"vim\"", true},
		{"bash -c \"echo hi && ssh host\"", true},
		{"bash -c \"echo safe\"", false},
	}
	for _, tc := range cases {
		_, got := detectInteractiveCommand(tc.cmd)
		if got != tc.want {
			t.Errorf("detectInteractiveCommand(%q)=%v want %v", tc.cmd, got, tc.want)
		}
	}
}
