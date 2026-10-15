package providers

import (
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// Endpoint security helpers for custom providers. Custom base URLs are
// operator-supplied and may legitimately point at local inference servers
// (localhost, RFC1918), so these helpers warn rather than block — except
// for cloud metadata endpoints, which are never legitimate model servers.

// metadataHosts are cloud instance-metadata endpoints. A provider base URL
// pointing here almost certainly means a misconfiguration that would send
// credentials and prompt content to infrastructure that echoes cloud
// credentials.
var metadataHosts = map[string]struct{}{
	"169.254.169.254":                {},
	"metadata.google.internal":       {},
	"metadata.google.internal.":      {},
	"instance-data":                  {},
	"instance-data.compute.internal": {},
}

// isLoopbackHostname reports whether host is a loopback name or address.
// Used by the redirect policy (https→http downgrade is tolerated for
// local servers) and by diagnostics.
func isLoopbackHostname(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.TrimSuffix(h, ".")
	if h == "localhost" {
		return true
	}
	if ip, err := netip.ParseAddr(stripZone(h)); err == nil {
		return ip.IsLoopback()
	}
	if ip := net.ParseIP(stripZone(h)); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// IsMetadataHost reports whether host is a known cloud metadata endpoint.
func IsMetadataHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.TrimSuffix(h, ".")
	if _, ok := metadataHosts[h]; ok {
		return true
	}
	if ip, err := netip.ParseAddr(stripZone(h)); err == nil {
		if ip.Is4() && ip.String() == "169.254.169.254" {
			return true
		}
	}
	return false
}

// IsPrivateEndpointHost reports whether host is loopback, RFC1918/link-local,
// or otherwise non-public. Hostnames that do not parse as IPs return false
// except for localhost — DNS names are left alone since their resolution
// can change between validation and connection (TOCTOU).
func IsPrivateEndpointHost(host string) bool {
	h := strings.TrimSpace(host)
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	if h == "localhost" {
		return true
	}
	addr, err := netip.ParseAddr(stripZone(h))
	if err != nil {
		return false
	}
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}

// EndpointHost extracts the hostname (no port, no brackets) from a base URL.
func EndpointHost(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// EndpointSecurityWarnings returns human-readable warnings for a custom
// endpoint. Empty means nothing noteworthy. Callers surface these in
// `ff provider test` and doctor output; they never block legitimate local
// servers.
func EndpointSecurityWarnings(baseURL string, hasAuth bool) []string {
	host := EndpointHost(baseURL)
	if host == "" {
		return nil
	}
	var out []string
	if IsMetadataHost(host) {
		out = append(out, "endpoint points at a cloud instance-metadata address ("+host+") - this is almost certainly a misconfiguration; refusing to guess credentials here")
		return out
	}
	if hasAuth && IsPrivateEndpointHost(host) {
		out = append(out, "endpoint "+host+" looks like a local/private address and requests carry an API key - this is fine for local inference servers, just confirming the key never leaves your network")
	} else if !hasAuth && IsPrivateEndpointHost(host) {
		out = append(out, "endpoint "+host+" looks like a local/private address without authentication - fine for local inference servers")
	}
	return out
}

func stripZone(h string) string {
	if i := strings.LastIndex(h, "%"); i >= 0 {
		return h[:i]
	}
	return h
}
