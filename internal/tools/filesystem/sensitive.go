package filesystem

import (
	"path/filepath"
	"strings"
)

// IsSensitivePath reports whether path looks like a credential or secret
// file that should not be auto-allowed. It does not rely on the model to
// recognize secrets — the check is purely lexical. Conservative: any
// suspicious name requires explicit permission.
func IsSensitivePath(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	// Normalize slashes for Windows + Unix
	slashPath := filepath.ToSlash(path)
	lowerPath := strings.ToLower(slashPath)
	base := filepath.Base(slashPath)
	lowerBase := strings.ToLower(base)

	// .env, .env.*, .env.local etc (any file starting with .env)
	if lowerBase == ".env" || strings.HasPrefix(lowerBase, ".env.") {
		return true
	}
	// Private keys / certs
	if strings.HasSuffix(lowerBase, ".pem") || strings.HasSuffix(lowerBase, ".key") {
		return true
	}
	if strings.HasSuffix(lowerBase, ".p12") || strings.HasSuffix(lowerBase, ".pfx") {
		return true
	}
	// SSH directory or files
	if strings.Contains(lowerPath, "/.ssh/") || strings.HasSuffix(lowerPath, "/.ssh") {
		return true
	}
	// Common SSH key filenames (even outside .ssh folder)
	switch lowerBase {
	case "id_rsa", "id_rsa.pub", "id_ed25519", "id_ed25519.pub", "id_ecdsa", "id_ecdsa.pub", "id_dsa", "authorized_keys", "known_hosts":
		return true
	}
	// Cloud credentials
	for _, seg := range []string{".aws", ".azure", ".gcloud", ".kube", ".docker"} {
		if strings.Contains(lowerPath, seg) {
			return true
		}
	}
	// Specific cloud credential files
	if lowerBase == "credentials" && strings.Contains(lowerPath, ".aws") {
		return true
	}
	if lowerBase == "config" && (strings.Contains(lowerPath, ".aws") || strings.Contains(lowerPath, ".kube")) {
		return true
	}
	if lowerBase == ".netrc" || lowerBase == ".git-credentials" {
		return true
	}
	// Generic secrets
	if lowerBase == ".npmrc" || lowerBase == ".pypirc" {
		return true
	}
	return false
}
