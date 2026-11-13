package web

import (
	"net/url"
	"strings"
)

// trackingParams is the set of query parameter names that are stripped during
// URL normalization because they carry cross-site tracking information.
var trackingParams = map[string]bool{
	// Google Analytics / Ads
	"utm_source":           true,
	"utm_medium":           true,
	"utm_campaign":         true,
	"utm_term":             true,
	"utm_content":          true,
	"utm_id":               true,
	"utm_source_platform":  true,
	"utm_creative_format":  true,
	"utm_marketing_tactic": true,
	// Google Ads
	"gclid":  true,
	"gclsrc": true,
	"dclid":  true,
	// Facebook
	"fbclid": true,
	// Microsoft / Bing
	"msclkid": true,
	// Twitter / X
	"twclid": true,
	// LinkedIn
	"li_fat_id": true,
	// HubSpot
	"hsa_acc": true,
	"hsa_cam": true,
	"hsa_grp": true,
	"hsa_ad":  true,
	"hsa_src": true,
	"hsa_tgt": true,
	"hsa_kw":  true,
	"hsa_mt":  true,
	"hsa_net": true,
	"hsa_ver": true,
	// Mailchimp
	"mc_cid": true,
	"mc_eid": true,
	// Adobe
	"ef_id":   true,
	"s_kwcid": true,
	// Misc
	"ref": true,
	"_ga": true,
	"_gl": true,
}

// NormalizeURL returns a canonical form of rawURL suitable for caching and
// deduplication. It:
//   - Parses the URL.
//   - Lower-cases the scheme and host.
//   - Removes default ports (80 for http, 443 for https).
//   - Strips tracking query parameters (utm_*, gclid, fbclid, etc.).
//   - Removes the fragment.
//   - Leaves the path and remaining query parameters intact.
//
// Returns the normalized URL string, or an error if rawURL is not parseable.
func NormalizeURL(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", &url.Error{Op: "normalize", URL: rawURL, Err: url.EscapeError("missing scheme or host")}
	}

	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)

	// Strip default ports.
	host, port, err2 := splitHostPort(u.Host)
	if err2 == nil {
		if (u.Scheme == "http" && port == "80") ||
			(u.Scheme == "https" && port == "443") {
			u.Host = host
		}
	}

	// Strip tracking query parameters.
	q := u.Query()
	changed := false
	for k := range q {
		lower := strings.ToLower(k)
		if trackingParams[lower] || strings.HasPrefix(lower, "utm_") {
			delete(q, k)
			changed = true
		}
	}
	if changed {
		u.RawQuery = q.Encode()
	}

	// Drop fragment.
	u.Fragment = ""
	u.RawFragment = ""

	return u.String(), nil
}

// splitHostPort is a minimal helper that separates host and port from a host
// string that may contain brackets (IPv6).
func splitHostPort(hostport string) (host, port string, err error) {
	if strings.Contains(hostport, ":") {
		// Could be IPv6 without port or host:port. Try net.SplitHostPort logic.
		if strings.HasPrefix(hostport, "[") {
			// IPv6 literal with possible port.
			end := strings.LastIndex(hostport, "]")
			if end < 0 {
				return "", "", url.EscapeError("missing ]")
			}
			host = hostport[1:end]
			rest := hostport[end+1:]
			if rest == "" {
				return host, "", nil
			}
			if rest[0] != ':' {
				return "", "", url.EscapeError("unexpected char after ]")
			}
			return host, rest[1:], nil
		}
		// Plain host:port
		idx := strings.LastIndex(hostport, ":")
		return hostport[:idx], hostport[idx+1:], nil
	}
	return hostport, "", url.EscapeError("no port")
}

// UnwrapDDGRedirect detects DuckDuckGo redirect URLs of the form
// https://duckduckgo.com/l/?uddg=<encoded-url>&... and returns the target URL.
// If the input is not a DDG redirect, the original rawURL is returned unchanged.
func UnwrapDDGRedirect(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	host := strings.ToLower(u.Host)
	if host != "duckduckgo.com" && !strings.HasSuffix(host, ".duckduckgo.com") {
		return rawURL
	}
	// /l/ path is the redirect endpoint.
	if u.Path != "/l/" && u.Path != "/l" {
		return rawURL
	}
	target := u.Query().Get("uddg")
	if target == "" {
		return rawURL
	}
	decoded, err := url.QueryUnescape(target)
	if err != nil {
		return target
	}
	return decoded
}

// ResolveRelativeURL resolves ref against base. If ref is already absolute it
// is returned unchanged (after normalization). base must be an absolute URL.
func ResolveRelativeURL(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	resolved := b.ResolveReference(r)
	return resolved.String(), nil
}

// StripTrackingParams removes known tracking query parameters from rawURL and
// returns the modified URL string. The URL must be valid.
func StripTrackingParams(rawURL string) (string, error) {
	return NormalizeURL(rawURL)
}
