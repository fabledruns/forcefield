package compare

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The comparison is a persisted document, so it has to survive a round
// trip through a file and come back as the same decisions.
func TestDocumentRoundTrip(t *testing.T) {
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 2, 20), stable(140, 2, 20))
	c := comparePair(t, base, cand, Options{})

	path := filepath.Join(t.TempDir(), "comparison.json")
	if err := c.Write(path); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.MethodVersion != c.MethodVersion || got.SchemaVersion != c.SchemaVersion {
		t.Errorf("identity changed on the round trip: %+v", got)
	}
	if len(got.Metrics) != len(c.Metrics) {
		t.Fatalf("metrics = %d, want %d", len(got.Metrics), len(c.Metrics))
	}
	if got.Metrics[0].Verdict != c.Metrics[0].Verdict {
		t.Errorf("verdict = %q, want %q", got.Metrics[0].Verdict, c.Metrics[0].Verdict)
	}
	if got.Method.ThresholdSource != c.Method.ThresholdSource ||
		got.Method.ThresholdVersion != c.Method.ThresholdVersion {
		t.Errorf("the threshold table identity must survive: %+v", got.Method)
	}
	if got.Method.ThresholdVersion == "" {
		t.Error("a stored verdict must name the threshold table it used")
	}
}

// Reading a document written by different rules must fail rather than
// hand back verdicts whose meaning has moved.
func TestReadRejectsForeignDecisions(t *testing.T) {
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 2, 20), stable(140, 2, 20))
	c := comparePair(t, base, cand, Options{})
	dir := t.TempDir()

	write := func(name string, mutate func(raw string) string) string {
		path := filepath.Join(dir, name)
		c.Write(path)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		edited := mutate(string(raw))
		if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	cases := []struct {
		name    string
		path    string
		wantsIn string
	}{
		{"other schema", write("a.json", func(s string) string {
			return strings.Replace(s, `"hpov.compare"`, `"hpov.something"`, 1)
		}), "schema"},
		{"other major version", write("b.json", func(s string) string {
			return strings.Replace(s, `"schema_version": "1.0.0"`, `"schema_version": "2.0.0"`, 1)
		}), "major"},
		{"other method version", write("c.json", func(s string) string {
			return strings.Replace(s, `"method_version": "1"`, `"method_version": "2"`, 1)
		}), "method"},
	}
	for _, tc := range cases {
		_, err := Read(tc.path)
		if err == nil {
			t.Errorf("%s: must be refused", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantsIn) {
			t.Errorf("%s: error %q must mention %q", tc.name, err, tc.wantsIn)
		}
	}
}

func TestReadRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Error("a malformed document must not be read")
	}
}
