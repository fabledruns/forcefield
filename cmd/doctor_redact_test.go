package cmd

import (
	"strings"
	"testing"

	"forcefield/internal/redact"
)

// TestDoctorLineScrubsSecrets pins that every diagnostic line is
// redacted before printing, even when a probe error echoes credentials.
func TestDoctorLineScrubsSecrets(t *testing.T) {
	redact.ResetSecrets()
	t.Cleanup(redact.ResetSecrets)
	redact.AddSecret("doctor-probe-secret-12345")

	line := doctorLine(vFail, "%s: could not reach %s (%v) with key %s",
		"OpenAI", "https://api.example.com",
		"dial failed echoing doctor-probe-secret-12345",
		"sk-12345678901234567890abcdef")
	if strings.Contains(line, "doctor-probe-secret-12345") {
		t.Errorf("registered value leaked: %q", line)
	}
	if strings.Contains(line, "sk-12345678901234567890abcdef") {
		t.Errorf("pattern secret leaked: %q", line)
	}
	if !strings.Contains(line, "[FAIL]") || !strings.Contains(line, "OpenAI") {
		t.Errorf("line lost its diagnostic content: %q", line)
	}
}
