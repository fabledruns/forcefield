package fixture

import (
	"os"
	"testing"
)

func TestRunRootAndDirs(t *testing.T) {
	root, err := NewRunRoot("")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	home, err := NewHome(root, "home-0")
	if err != nil {
		t.Fatal(err)
	}
	work, err := NewWorkDir(root, "work-0")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{root, home, work} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Fatalf("%s not a dir", d)
		}
	}
}

func TestScrubEnv(t *testing.T) {
	t.Setenv("GIT_DIR", "/tmp/x")
	t.Setenv("FF_PERF_MARKERS", "1")
	t.Setenv("HTTP_PROXY", "http://proxy")
	env, removed := ScrubEnv(map[string]string{"HPOV_TEST_SET": "1"})
	joined := ""
	for _, kv := range env {
		joined += kv + "\n"
	}
	for _, bad := range []string{"GIT_DIR=", "FF_PERF_MARKERS=", "HTTP_PROXY="} {
		if contains(joined, bad) {
			t.Fatalf("scrubbed env leaks %q", bad)
		}
	}
	if !contains(joined, "HPOV_TEST_SET=1") {
		t.Fatal("override missing from env")
	}
	if !contains(joined, "PATH=") {
		t.Fatal("PATH must survive scrubbing")
	}
	for _, want := range []string{"GIT_DIR", "FF_PERF_MARKERS", "HTTP_PROXY"} {
		found := false
		for _, r := range removed {
			if r == want {
				found = true
			}
		}
		if !found {
			t.Errorf("removed does not record %s (got %v)", want, removed)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
