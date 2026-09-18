package runtime

import "testing"

func TestJobSnapshots_NilRuntimeIsNil(t *testing.T) {
	var r *Runtime
	if out := r.JobSnapshots(); out != nil {
		t.Fatalf("nil JobSnapshots = %v, want nil", out)
	}
}

func TestJobSnapshots_MissingToolIsNil(t *testing.T) {
	r := newTestRuntime(&scriptedProvider{turns: testTurns()})
	if out := r.JobSnapshots(); out != nil {
		t.Fatalf("JobSnapshots without shell_job = %v, want nil", out)
	}
}
