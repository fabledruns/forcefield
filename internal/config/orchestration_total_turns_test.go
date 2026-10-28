package config

import (
	"strings"
	"testing"
)

// TestValidateOrchestrationTotalTurns pins the range for the aggregate
// child-turn budget: 0/omit = default, explicit values must fall
// inside 1..MaxOrchestrationTurns, and rejections name the field.
func TestValidateOrchestrationTotalTurns(t *testing.T) {
	withTurns := func(v int) Orchestration {
		vv := v
		return Orchestration{MaxTotalChildTurns: &vv}
	}
	for _, v := range []int{-1, 0, 8193} {
		err := validateOrchestration(withTurns(v))
		if err == nil {
			t.Errorf("v=%d: expected rejection", v)
			continue
		}
		if !strings.Contains(err.Error(), "max_total_child_turns") {
			t.Errorf("v=%d: error %q must name the field", v, err.Error())
		}
	}
	for _, v := range []int{1, 2048, 8192} {
		if err := validateOrchestration(withTurns(v)); err != nil {
			t.Errorf("v=%d: unexpected error %v", v, err)
		}
	}
	if err := validateOrchestration(Orchestration{}); err != nil {
		t.Errorf("omitted field must stay valid: %v", err)
	}
}
