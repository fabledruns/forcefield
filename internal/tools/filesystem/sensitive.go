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
	// Always normalize backslashes to forward slashes for cross-platform
	// consistency (filepath.ToSlash is a no-op on Unix, but paths containing
	// Windows backslashes can still be passed on Unix or in tests).
	slashPath := strings.ReplaceAll(path, "\\", "/")

	// Strip volume name (e.g. C:) if present before inspecting streams.
	// On Unix, filepath.VolumeName returns "" for Windows drive letters, so
	// we explicitly detect Windows drive letters (e.g. "C:") here.
	vol := ""
	if len(slashPath) >= 2 && slashPath[1] == ':' && ((slashPath[0] >= 'a' && slashPath[0] <= 'z') || (slashPath[0] >= 'A' && slashPath[0] <= 'Z')) {
		vol = slashPath[:2]
	} else {
		vol = filepath.VolumeName(slashPath)
	}
	noVol := slashPath[len(vol):]

	// Strip NTFS alternate data stream specifiers (e.g. :$DATA, :stream_name, :stream:$DATA).
	// Since the volume prefix has been stripped, any colon in noVol is an NTFS stream delimiter.
	if idx := strings.IndexByte(noVol, ':'); idx >= 0 {
		noVol = noVol[:idx]
	}

	lowerPath := strings.ToLower(vol + noVol)
	base := noVol
	if idx := strings.LastIndexByte(noVol, '/'); idx >= 0 {
		base = noVol[idx+1:]
	}
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
	if lowerBase == ".ssh" || strings.Contains(lowerPath, "/.ssh/") || strings.HasSuffix(lowerPath, "/.ssh") || lowerPath == ".ssh" {
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
