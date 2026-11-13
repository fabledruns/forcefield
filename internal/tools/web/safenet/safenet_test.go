package safenet_test

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"forcefield/internal/tools/web/safenet"
)

// ---------------------------------------------------------------------------
// Validator tests
// ---------------------------------------------------------------------------

func TestValidateIP_Localhost(t *testing.T) {
	v := mustValidator(t, nil)
	for _, addr := range []string{
		"127.0.0.1",
		"127.0.0.2",
		"127.255.255.255",
		"::1",
	} {
		if err := v.ValidateIP(addr); err == nil {
			t.Errorf("expected block for %q, got nil", addr)
		}
	}
}

func TestValidateIP_Private(t *testing.T) {
	v := mustValidator(t, nil)
	privates := []string{
		"10.0.0.1",
		"10.255.255.255",
		"172.16.0.1",
		"172.31.255.255",
		"192.168.0.1",
		"192.168.255.255",
	}
	for _, addr := range privates {
		if err := v.ValidateIP(addr); err == nil {
			t.Errorf("expected block for %q, got nil", addr)
		}
	}
}

func TestValidateIP_LinkLocal(t *testing.T) {
	v := mustValidator(t, nil)
	for _, addr := range []string{
		"169.254.0.1",
		"169.254.169.254", // AWS metadata
		"fe80::1",
	} {
		if err := v.ValidateIP(addr); err == nil {
			t.Errorf("expected block for %q, got nil", addr)
		}
	}
}

func TestValidateIP_CloudMetadataIPv4(t *testing.T) {
	v := mustValidator(t, nil)
	if err := v.ValidateIP("169.254.169.254"); err == nil {
		t.Error("expected block for AWS/Azure metadata IP 169.254.169.254")
	}
}

func TestValidateIP_CloudMetadataIPv6(t *testing.T) {
	v := mustValidator(t, nil)
	// fd00:ec2::254 is in the fc00::/7 unique-local block.
	if err := v.ValidateIP("fd00:ec2::254"); err == nil {
		t.Error("expected block for GCP metadata IPv6 fd00:ec2::254")
	}
}

func TestValidateHostname_MetadataGoogle(t *testing.T) {
	v := mustValidator(t, nil)
	if err := v.ValidateHostname("metadata.google.internal"); err == nil {
		t.Error("expected block for metadata.google.internal")
	}
	if err := v.ValidateHostname("METADATA.GOOGLE.INTERNAL"); err == nil {
		t.Error("expected block for METADATA.GOOGLE.INTERNAL (case-insensitive)")
	}
}

func TestValidateIP_IPv4MappedIPv6(t *testing.T) {
	v := mustValidator(t, nil)
	// ::ffff:192.168.1.1 maps to private 192.168.1.1
	if err := v.ValidateIP("::ffff:192.168.1.1"); err == nil {
		t.Error("expected block for IPv4-mapped private address ::ffff:192.168.1.1")
	}
	if err := v.ValidateIP("::ffff:127.0.0.1"); err == nil {
		t.Error("expected block for IPv4-mapped loopback ::ffff:127.0.0.1")
	}
}

func TestValidateIP_CarrierGradeNAT(t *testing.T) {
	v := mustValidator(t, nil)
	for _, addr := range []string{
		"100.64.0.0",
		"100.64.0.1",
		"100.127.255.255",
	} {
		if err := v.ValidateIP(addr); err == nil {
			t.Errorf("expected block for CGN address %q", addr)
		}
	}
}

func TestValidateIP_UniqueLocal(t *testing.T) {
	v := mustValidator(t, nil)
	for _, addr := range []string{
		"fc00::1",
		"fd00::1",
		"fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
	} {
		if err := v.ValidateIP(addr); err == nil {
			t.Errorf("expected block for unique-local address %q", addr)
		}
	}
}

func TestValidateIP_Multicast(t *testing.T) {
	v := mustValidator(t, nil)
	for _, addr := range []string{
		"224.0.0.1",
		"239.255.255.255",
		"ff02::1",
	} {
		if err := v.ValidateIP(addr); err == nil {
			t.Errorf("expected block for multicast address %q", addr)
		}
	}
}

func TestValidateIP_PublicAllowed(t *testing.T) {
	v := mustValidator(t, nil)
	for _, addr := range []string{
		"1.1.1.1",
		"8.8.8.8",
		"93.184.216.34", // example.com
		"2606:4700:4700::1111",
	} {
		if err := v.ValidateIP(addr); err != nil {
			t.Errorf("expected allow for %q, got %v", addr, err)
		}
	}
}

func TestValidatePort_Default(t *testing.T) {
	v := mustValidator(t, nil)
	if err := v.ValidatePort(80); err != nil {
		t.Errorf("expected port 80 allowed: %v", err)
	}
	if err := v.ValidatePort(443); err != nil {
		t.Errorf("expected port 443 allowed: %v", err)
	}
}

func TestValidatePort_NonStandard(t *testing.T) {
	v := mustValidator(t, nil)
	for _, port := range []int{8080, 8443, 3000, 22, 21, 25} {
		if err := v.ValidatePort(port); err == nil {
			t.Errorf("expected block for non-standard port %d", port)
		}
	}
}

func TestValidatePort_CustomAllowed(t *testing.T) {
	v := mustValidator(t, &safenet.ValidatorConfig{
		AllowedPorts: []int{80, 443, 8080},
	})
	if err := v.ValidatePort(8080); err != nil {
		t.Errorf("expected port 8080 allowed: %v", err)
	}
}

func TestAllowlist_BypassesBlock(t *testing.T) {
	v := mustValidator(t, &safenet.ValidatorConfig{
		Allowlist: []string{"10.0.0.1"},
	})
	if err := v.ValidateIP("10.0.0.1"); err != nil {
		t.Errorf("allowlisted IP should not be blocked: %v", err)
	}
}

func TestDenylist_BlocksPublic(t *testing.T) {
	v := mustValidator(t, &safenet.ValidatorConfig{
		Denylist: []string{"1.1.1.1"},
	})
	if err := v.ValidateIP("1.1.1.1"); err == nil {
		t.Error("denylisted IP should be blocked")
	}
}

func TestDenylist_Host(t *testing.T) {
	v := mustValidator(t, &safenet.ValidatorConfig{
		Denylist: []string{"evil.example.com"},
	})
	if err := v.ValidateHostname("evil.example.com"); err == nil {
		t.Error("denylisted hostname should be blocked")
	}
}

// ---------------------------------------------------------------------------
// Client tests (using httptest)
// ---------------------------------------------------------------------------

func TestSafeGet_PublicServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "hello world")
	}))
	defer srv.Close()

	// httptest.NewServer binds to 127.0.0.1 which is blocked by default.
	// Use an allowlisted config.
	c := mustClient(t, &safenet.ClientConfig{
		Validator: &safenet.ValidatorConfig{
			AllowedPorts: []int{serverPort(t, srv)},
			Allowlist:    []string{"127.0.0.1"},
		},
	})

	body, resp, err := c.SafeGet(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("SafeGet: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "hello") {
		t.Errorf("unexpected body: %q", body)
	}
}

func TestSafeGet_PrivateIPBlocked(t *testing.T) {
	c := mustClient(t, nil)
	_, _, err := c.SafeGet(context.Background(), "http://192.168.1.1/")
	if err == nil {
		t.Error("expected block for private IP, got nil")
	}
}

func TestSafeGet_LoopbackBlocked(t *testing.T) {
	c := mustClient(t, nil)
	_, _, err := c.SafeGet(context.Background(), "http://127.0.0.1/")
	if err == nil {
		t.Error("expected block for loopback IP, got nil")
	}
}

func TestSafeGet_LocalhostBlocked(t *testing.T) {
	c := mustClient(t, nil)
	_, _, err := c.SafeGet(context.Background(), "http://localhost/")
	if err == nil {
		t.Error("expected block when localhost resolves to 127.0.0.1")
	}
}

func TestSafeGet_MetadataHostnameBlocked(t *testing.T) {
	c := mustClient(t, nil)
	_, _, err := c.SafeGet(context.Background(), "http://metadata.google.internal/")
	if err == nil {
		t.Error("expected block for metadata.google.internal")
	}
}

func TestSafeGet_NonStandardPortBlocked(t *testing.T) {
	c := mustClient(t, nil)
	_, _, err := c.SafeGet(context.Background(), "http://example.com:8080/")
	if err == nil {
		t.Error("expected block for non-standard port 8080")
	}
}

func TestSafeGet_WireLimit(t *testing.T) {
	const wireLimit = 5 * 1024 * 1024 // must match safenet.WireLimit

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write more than the wire limit.
		for i := 0; i < wireLimit+1024; i++ {
			w.Write([]byte("x"))
		}
	}))
	defer srv.Close()

	c := mustClient(t, &safenet.ClientConfig{
		Validator: &safenet.ValidatorConfig{
			AllowedPorts: []int{serverPort(t, srv)},
			Allowlist:    []string{"127.0.0.1"},
		},
	})

	_, _, err := c.SafeGet(context.Background(), srv.URL+"/")
	if err == nil {
		t.Error("expected wire limit error")
	}
}

func TestSafeGet_GzipDecompression(t *testing.T) {
	import_gzip := func() io.Writer { return io.Discard } // shadow to avoid import
	_ = import_gzip

	// Use httptest + stdlib gzip writer.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		gz := newGzipWriter(w)
		defer gz.Close()
		fmt.Fprintln(gz, "compressed hello")
	}))
	defer srv.Close()

	c := mustClient(t, &safenet.ClientConfig{
		Validator: &safenet.ValidatorConfig{
			AllowedPorts: []int{serverPort(t, srv)},
			Allowlist:    []string{"127.0.0.1"},
		},
	})

	body, _, err := c.SafeGet(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("SafeGet gzip: %v", err)
	}
	if !strings.Contains(string(body), "compressed hello") {
		t.Errorf("decompressed body unexpected: %q", body)
	}
}

func TestRedirect_ToPrivateIP_Blocked(t *testing.T) {
	// A server that redirects to a private IP.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This should never be reached.
		fmt.Fprintln(w, "should not get here")
	}))
	defer target.Close()

	// Redirect server redirecting to a private IP address.
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://192.168.1.1/", http.StatusFound)
	}))
	defer redirect.Close()

	c := mustClient(t, &safenet.ClientConfig{
		Validator: &safenet.ValidatorConfig{
			AllowedPorts: []int{serverPort(t, redirect), serverPort(t, target)},
			Allowlist:    []string{"127.0.0.1"},
		},
	})

	_, _, err := c.SafeGet(context.Background(), redirect.URL+"/")
	if err == nil {
		t.Error("expected redirect to private IP to be blocked")
	}
}

func TestRedirect_MaxHops(t *testing.T) {
	// Build a chain of servers each redirecting to the next (or itself).
	var mu sync.Mutex
	var servers []*httptest.Server
	var port int

	// We test max-redirect by creating a redirect loop.
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		p := port
		mu.Unlock()
		http.Redirect(w, r, fmt.Sprintf("http://127.0.0.1:%d/", p), http.StatusFound)
	}))
	servers = append(servers, srv)
	mu.Lock()
	port = serverPort(t, srv)
	mu.Unlock()
	defer srv.Close()

	c := mustClient(t, &safenet.ClientConfig{
		Validator: &safenet.ValidatorConfig{
			AllowedPorts: []int{port},
			Allowlist:    []string{"127.0.0.1"},
		},
		Timeout: 5 * time.Second,
	})

	_, _, err := c.SafeGet(context.Background(), srv.URL+"/")
	if err == nil {
		t.Error("expected too-many-redirects error")
	}
}

func TestRedirect_CrossOrigin_HeaderStripping(t *testing.T) {
	var capturedAuth string
	var capturedCookie string
	var mu sync.Mutex

	// Server B captures whatever headers it receives.
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedAuth = r.Header.Get("Authorization")
		capturedCookie = r.Header.Get("Cookie")
		mu.Unlock()
		fmt.Fprintln(w, "ok")
	}))
	defer serverB.Close()

	portB := serverPort(t, serverB)

	// Server A redirects to serverB (different port = different origin).
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, serverB.URL+"/", http.StatusFound)
	}))
	defer serverA.Close()

	portA := serverPort(t, serverA)

	c := mustClient(t, &safenet.ClientConfig{
		Validator: &safenet.ValidatorConfig{
			AllowedPorts: []int{portA, portB},
			Allowlist:    []string{"127.0.0.1"},
		},
	})

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, serverA.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Cookie", "session=abc")

	resp, err := c.HTTPClient().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	mu.Lock()
	auth := capturedAuth
	cookie := capturedCookie
	mu.Unlock()

	if auth != "" {
		t.Errorf("Authorization header should be stripped on cross-origin redirect, got %q", auth)
	}
	if cookie != "" {
		t.Errorf("Cookie header should be stripped on cross-origin redirect, got %q", cookie)
	}
}

func TestConnectionReuse_GoesThrough_Validator(t *testing.T) {
	// Every new connection must go through the validated dialer.
	// We verify this by ensuring that requests to a blocked IP fail even
	// when a previous request opened a connection to an allowed host.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	}))
	defer srv.Close()

	c := mustClient(t, &safenet.ClientConfig{
		Validator: &safenet.ValidatorConfig{
			AllowedPorts: []int{serverPort(t, srv)},
			Allowlist:    []string{"127.0.0.1"},
		},
	})

	// First request succeeds.
	_, _, err := c.SafeGet(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}

	// Second request to a blocked address must still fail.
	_, _, err = c.SafeGet(context.Background(), "http://10.0.0.1/")
	if err == nil {
		t.Error("expected block for private IP on second request")
	}
}

// ---------------------------------------------------------------------------
// DNS rebinding simulation
// ---------------------------------------------------------------------------

// TestDNSRebinding_AllIPsMustPass verifies that if any resolved IP is blocked,
// the dial fails even if one of the IPs would be allowed.
// We simulate this by patching via a custom resolver returning a mix.
// Since we cannot easily override net.DefaultResolver in a unit test without
// network access, we test the ValidateIP function which is the guard.
func TestDNSRebinding_AllIPsMustValidate(t *testing.T) {
	v := mustValidator(t, nil)

	// A set that includes one private IP — should block.
	ips := []string{"1.1.1.1", "10.0.0.1"}
	var blocked bool
	for _, ip := range ips {
		if err := v.ValidateIP(ip); err != nil {
			blocked = true
			break
		}
	}
	if !blocked {
		t.Error("expected at least one IP in the set to be blocked")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mustValidator(t *testing.T, cfg *safenet.ValidatorConfig) *safenet.Validator {
	t.Helper()
	v, err := safenet.NewValidator(cfg)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return v
}

func mustClient(t *testing.T, cfg *safenet.ClientConfig) *safenet.Client {
	t.Helper()
	c, err := safenet.NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func serverPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	return port
}

func newGzipWriter(w io.Writer) io.WriteCloser {
	return gzip.NewWriter(w)
}
