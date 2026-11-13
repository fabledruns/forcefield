package safenet

import (
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// WireLimit is the maximum number of bytes read from the network response body.
	WireLimit = 5 * 1024 * 1024 // 5 MiB

	// DecompressedLimit is the maximum number of bytes after decompression.
	DecompressedLimit = 10 * 1024 * 1024 // 10 MiB

	// MaxRedirects is the maximum number of redirects followed.
	MaxRedirects = 10
)

// ClientConfig configures the safe HTTP client.
type ClientConfig struct {
	// Validator configures IP/hostname validation rules.
	// If nil, NewValidator(nil) is used (ports 80 and 443 only).
	Validator *ValidatorConfig

	// Timeout for the entire request, including redirects.
	// Defaults to 30 seconds.
	Timeout time.Duration

	// UserAgent to send in requests.
	UserAgent string
}

// Client is a hardened HTTP client that prevents SSRF and DNS rebinding.
type Client struct {
	validator  *Validator
	httpClient *http.Client
	userAgent  string
}

// NewClient creates a new safe HTTP client.
// A nil config uses defaults.
func NewClient(cfg *ClientConfig) (*Client, error) {
	var vcfg *ValidatorConfig
	timeout := 30 * time.Second
	ua := "Forcefield/1 (+https://github.com/fabledruns/forcefield)"

	if cfg != nil {
		vcfg = cfg.Validator
		if cfg.Timeout > 0 {
			timeout = cfg.Timeout
		}
		if cfg.UserAgent != "" {
			ua = cfg.UserAgent
		}
	}

	v, err := NewValidator(vcfg)
	if err != nil {
		return nil, fmt.Errorf("safenet: creating validator: %w", err)
	}

	c := &Client{
		validator: v,
		userAgent: ua,
	}

	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	transport := &http.Transport{
		// Use our custom DialContext that validates IPs and pins to a resolved address.
		DialContext:           c.safeDialContext(dialer),
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Disable HTTP/2 DialTLS so we keep full control via DialContext.
		ForceAttemptHTTP2: false,
		// Limit connection pooling to prevent cross-request IP reuse attacks.
		MaxIdleConns:       100,
		IdleConnTimeout:    90 * time.Second,
		MaxConnsPerHost:    10,
		DisableCompression: true, // We handle decompression ourselves.
	}

	c.httpClient = &http.Client{
		Timeout:       timeout,
		Transport:     transport,
		CheckRedirect: c.checkRedirect,
	}

	return c, nil
}

// safeDialContext returns a DialContext function that:
//  1. Resolves the hostname to IP addresses using the system resolver.
//  2. Validates every resolved IP against SSRF policy.
//  3. Dials directly to the first validated IP, bypassing further DNS
//     resolution (preventing DNS rebinding TOCTOU).
//  4. Configures TLS SNI to the original hostname.
func (c *Client) safeDialContext(dialer *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("safenet: invalid address %q: %w", addr, err)
		}

		// Validate hostname itself first (e.g. metadata.google.internal).
		if err := c.validator.ValidateHostname(host); err != nil {
			return nil, err
		}

		// Validate the port.
		portNum, err := net.LookupPort("tcp", port)
		if err != nil {
			return nil, fmt.Errorf("safenet: invalid port %q: %w", port, err)
		}
		if err := c.validator.ValidatePort(portNum); err != nil {
			return nil, err
		}

		// Resolve DNS.
		var addrs []string
		if ip := net.ParseIP(host); ip != nil {
			// Already an IP address literal.
			addrs = []string{host}
		} else {
			resolved, err := net.DefaultResolver.LookupHost(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("safenet: DNS lookup %q: %w", host, err)
			}
			if len(resolved) == 0 {
				return nil, fmt.Errorf("safenet: DNS lookup %q: no addresses", host)
			}
			addrs = resolved
		}

		// Validate every resolved IP. All must pass (defense-in-depth).
		for _, ip := range addrs {
			if err := c.validator.ValidateIP(ip); err != nil {
				return nil, err
			}
		}

		// Pin to the first validated IP to prevent DNS rebinding.
		pinnedAddr := net.JoinHostPort(addrs[0], port)
		conn, err := dialer.DialContext(ctx, network, pinnedAddr)
		if err != nil {
			return nil, fmt.Errorf("safenet: dial %s: %w", pinnedAddr, err)
		}
		return conn, nil
	}
}

// checkRedirect validates each redirect hop and strips sensitive headers on
// cross-origin redirects.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= MaxRedirects {
		return fmt.Errorf("safenet: too many redirects (max %d)", MaxRedirects)
	}

	// Validate the redirect destination host.
	host := req.URL.Hostname()
	if err := c.validator.ValidateHostname(host); err != nil {
		return fmt.Errorf("safenet: redirect blocked: %w", err)
	}

	portStr := req.URL.Port()
	if portStr == "" {
		switch strings.ToLower(req.URL.Scheme) {
		case "http":
			portStr = "80"
		case "https":
			portStr = "443"
		default:
			return fmt.Errorf("safenet: redirect to unsupported scheme %q", req.URL.Scheme)
		}
	}
	portNum, err := net.LookupPort("tcp", portStr)
	if err != nil {
		return fmt.Errorf("safenet: redirect invalid port: %w", err)
	}
	if err := c.validator.ValidatePort(portNum); err != nil {
		return fmt.Errorf("safenet: redirect blocked: %w", err)
	}

	// Strip Authorization and Cookie headers on cross-origin redirects.
	if len(via) > 0 {
		origOrigin := origin(via[0].URL)
		newOrigin := origin(req.URL)
		if origOrigin != newOrigin {
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
		}
	}

	return nil
}

// origin returns the scheme+host+port canonical origin for a URL.
func origin(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

// SafeGet performs a validated HTTP GET request and returns the response body
// subject to WireLimit (5 MiB) and DecompressedLimit (10 MiB).
//
// It validates the target host+port before making the request. The
// Content-Encoding is handled transparently.
func (c *Client) SafeGet(ctx context.Context, rawURL string) ([]byte, *http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, fmt.Errorf("safenet: invalid URL %q: %w", rawURL, err)
	}

	// Pre-validate hostname and port before even sending the request.
	host := u.Hostname()
	if err := c.validator.ValidateHostname(host); err != nil {
		return nil, nil, err
	}

	portStr := u.Port()
	if portStr == "" {
		switch strings.ToLower(u.Scheme) {
		case "http":
			portStr = "80"
		case "https":
			portStr = "443"
		default:
			return nil, nil, fmt.Errorf("safenet: unsupported scheme %q", u.Scheme)
		}
	}
	portNum, err := net.LookupPort("tcp", portStr)
	if err != nil {
		return nil, nil, fmt.Errorf("safenet: invalid port: %w", err)
	}
	if err := c.validator.ValidatePort(portNum); err != nil {
		return nil, nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("safenet: building request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept-Encoding", "gzip, deflate")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("safenet: HTTP GET %q: %w", rawURL, err)
	}
	defer resp.Body.Close()

	// Enforce wire limit.
	limitedBody := io.LimitReader(resp.Body, WireLimit+1)
	raw, err := io.ReadAll(limitedBody)
	if err != nil {
		return nil, resp, fmt.Errorf("safenet: reading response body: %w", err)
	}
	if int64(len(raw)) > WireLimit {
		return nil, resp, fmt.Errorf("safenet: response body exceeds wire limit (%d bytes)", WireLimit)
	}

	// Decompress if needed.
	body, err := decompress(resp, raw)
	if err != nil {
		return nil, resp, fmt.Errorf("safenet: decompressing response: %w", err)
	}
	if int64(len(body)) > DecompressedLimit {
		return nil, resp, fmt.Errorf("safenet: decompressed body exceeds limit (%d bytes)", DecompressedLimit)
	}

	return body, resp, nil
}

// SafeGetWithLimit performs a validated HTTP GET with an optional byte limit and returns
// the body bytes and final URL (after redirects).
func (c *Client) SafeGetWithLimit(ctx context.Context, rawURL string, maxBytes int) ([]byte, string, error) {
	body, resp, err := c.SafeGet(ctx, rawURL)
	if err != nil {
		return nil, "", err
	}
	finalURL := rawURL
	if resp != nil && resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	if maxBytes > 0 && len(body) > maxBytes {
		body = body[:maxBytes]
	}
	return body, finalURL, nil
}

// decompress decompresses raw bytes according to the Content-Encoding header.
func decompress(resp *http.Response, raw []byte) ([]byte, error) {
	enc := strings.ToLower(resp.Header.Get("Content-Encoding"))
	switch enc {
	case "gzip":
		r, err := gzip.NewReader(strings.NewReader(string(raw)))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		limited := io.LimitReader(r, DecompressedLimit+1)
		return io.ReadAll(limited)
	case "deflate":
		r := flate.NewReader(strings.NewReader(string(raw)))
		defer r.Close()
		limited := io.LimitReader(r, DecompressedLimit+1)
		return io.ReadAll(limited)
	default:
		return raw, nil
	}
}

// TLSDialer returns a DialTLS function suitable for use in http.Transport that
// validates the IP and sets ServerName for strict SNI verification.
//
// Note: this is provided for callers that need a custom TLS dialer. NewClient
// already handles TLS correctly through the DialContext + Transport.
func (c *Client) TLSDialer(dialer *net.Dialer) func(network, addr string) (net.Conn, error) {
	safeCtxDialer := c.safeDialContext(dialer)
	return func(network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		conn, err := safeCtxDialer(context.Background(), network, addr)
		if err != nil {
			return nil, err
		}
		tlsCfg := &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		}
		tlsConn := tls.Client(conn, tlsCfg)
		if err := tlsConn.Handshake(); err != nil {
			conn.Close()
			return nil, fmt.Errorf("safenet: TLS handshake: %w", err)
		}
		return tlsConn, nil
	}
}

// HTTPClient returns the underlying *http.Client for callers that need to set
// additional options (e.g. extra headers). The returned client is already
// configured with the safe dialer and redirect policy.
func (c *Client) HTTPClient() *http.Client {
	return c.httpClient
}
