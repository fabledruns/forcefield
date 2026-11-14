//go:build windows

package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forcefield/internal/config"
)

func TestDoctorLemonade_OfflineWindowsNoWSL2Hint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := srv.URL
	srv.Close()

	// Even if WSL_DISTRO_NAME is present in the Windows environment, native Windows
	// executables are not running inside WSL2 and must not emit WSL2 hints.
	t.Setenv("WSL_DISTRO_NAME", "Ubuntu-26.04")
	isolateProviderHome(t)
	writeProviderConfig(t, fmt.Sprintf(`model:
  provider: lemonade
  name: Qwen3-4B
providers:
  lemonade:
    type: lemonade
    base_url: %s/api/v1
`, closedURL))

	cfg, _ := config.Load()
	var lines []string
	doctorProvider(cfg, func(v verdict, format string, args ...any) {
		lines = append(lines, doctorLine(v, format, args...))
	})
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "[FAIL]") {
		t.Fatalf("expected [FAIL] for unreachable Lemonade on Windows, got:\n%s", joined)
	}
	if strings.Contains(joined, "networkingMode=mirrored") || strings.Contains(joined, "WSL2 note:") {
		t.Fatalf("did not expect WSL2 hint on native Windows, got:\n%s", joined)
	}
}
