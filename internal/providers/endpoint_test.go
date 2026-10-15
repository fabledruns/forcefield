package providers

import (
	"net/http"
	"net/url"
	"testing"
)

func TestSafeRedirectSameHostAllowed(t *testing.T) {
	first, _ := url.Parse("https://api.example.com/v1")
	next, _ := url.Parse("https://api.example.com/v1/models")
	req := &http.Request{URL: next}
	if err := safeRedirectCheck(req, []*http.Request{{URL: first}}); err != nil {
		t.Errorf("same-host redirect blocked: %v", err)
	}
}

func TestSafeRedirectCrossHostBlocked(t *testing.T) {
	first, _ := url.Parse("https://api.example.com/v1")
	next, _ := url.Parse("https://evil.example.net/steal")
	req := &http.Request{URL: next}
	if err := safeRedirectCheck(req, []*http.Request{{URL: first}}); err == nil {
		t.Fatal("cross-host redirect allowed, want block")
	}
}

func TestSafeRedirectDowngradeBlocked(t *testing.T) {
	first, _ := url.Parse("https://api.example.com/v1")
	next, _ := url.Parse("http://api.example.com/v1")
	req := &http.Request{URL: next}
	if err := safeRedirectCheck(req, []*http.Request{{URL: first}}); err == nil {
		t.Fatal("https→http downgrade allowed for public host, want block")
	}
}

func TestSafeRedirectDowngradeLoopbackAllowed(t *testing.T) {
	first, _ := url.Parse("https://localhost:1234/v1")
	next, _ := url.Parse("http://localhost:1234/v1")
	req := &http.Request{URL: next}
	if err := safeRedirectCheck(req, []*http.Request{{URL: first}}); err != nil {
		t.Errorf("loopback downgrade blocked: %v", err)
	}
}

func TestSafeRedirectTooManyBlocked(t *testing.T) {
	u, _ := url.Parse("https://api.example.com/a")
	req := &http.Request{URL: u}
	via := []*http.Request{{URL: u}, {URL: u}, {URL: u}}
	if err := safeRedirectCheck(req, via); err == nil {
		t.Fatal("redirect chain of 3 allowed, want block")
	}
}

func TestEndpointSecurityHelpers(t *testing.T) {
	if !IsMetadataHost("169.254.169.254") {
		t.Error("169.254.169.254 not detected as metadata")
	}
	if !IsMetadataHost("metadata.google.internal") {
		t.Error("metadata.google.internal not detected")
	}
	if IsMetadataHost("api.example.com") {
		t.Error("api.example.com wrongly detected as metadata")
	}
	if !IsPrivateEndpointHost("127.0.0.1") || !IsPrivateEndpointHost("localhost") {
		t.Error("loopback not detected as private")
	}
	if !IsPrivateEndpointHost("192.168.1.10") || !IsPrivateEndpointHost("10.0.0.5") {
		t.Error("RFC1918 not detected as private")
	}
	if IsPrivateEndpointHost("api.example.com") {
		t.Error("public hostname wrongly detected as private")
	}
	if w := EndpointSecurityWarnings("http://169.254.169.254/", true); len(w) == 0 {
		t.Error("metadata endpoint produced no warning")
	}
	if w := EndpointSecurityWarnings("http://localhost:1234/v1", true); len(w) == 0 {
		t.Error("local+auth endpoint produced no note")
	}
	if w := EndpointSecurityWarnings("https://api.example.com/v1", true); len(w) != 0 {
		t.Errorf("public endpoint warnings = %v, want none", w)
	}
	if got := EndpointHost("http://localhost:1234/v1"); got != "localhost" {
		t.Errorf("EndpointHost = %q, want localhost", got)
	}
}
