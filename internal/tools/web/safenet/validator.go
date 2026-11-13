// Package safenet provides SSRF and DNS rebinding protections for Forcefield's
// web tool family. It validates destination IPs and hostnames before any
// outbound connection is established.
package safenet

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// ErrBlockedDestination is returned when a resolved IP or hostname is blocked
// by SSRF / DNS-rebinding policy.
type ErrBlockedDestination struct {
	Addr   string
	Reason string
}

func (e *ErrBlockedDestination) Error() string {
	return fmt.Sprintf("safenet: blocked destination %q: %s", e.Addr, e.Reason)
}

// ErrBlockedPort is returned when the requested port is not in the allowed set.
type ErrBlockedPort struct {
	Port int
}

func (e *ErrBlockedPort) Error() string {
	return fmt.Sprintf("safenet: port %d not in allowed list", e.Port)
}

// DefaultAllowedPorts is the set of ports permitted by default.
var DefaultAllowedPorts = []int{80, 443}

// blockedCIDRs is the list of networks that must never be dialled.
var blockedCIDRs []netip.Prefix

func init() {
	raw := []string{
		// Loopback
		"127.0.0.0/8",
		"::1/128",
		// RFC 1918 private
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		// Link-local
		"169.254.0.0/16",
		"fe80::/10",
		// IPv4-mapped IPv6 (covers all of the above via ::ffff:)
		"::ffff:0:0/96",
		// Carrier-grade NAT (RFC 6598)
		"100.64.0.0/10",
		// Unique local (RFC 4193) – includes fd00:ec2::/32 and fd00::/8
		"fc00::/7",
		// Multicast
		"224.0.0.0/4",
		"ff00::/8",
		// Broadcast (limited)
		"255.255.255.255/32",
		// Reserved / documentation (RFC 5737)
		"192.0.2.0/24",
		"198.51.100.0/24",
		"203.0.113.0/24",
		// Reserved (IANA)
		"192.0.0.0/24",
		// Benchmarking (RFC 2544)
		"198.18.0.0/15",
		// Unspecified
		"0.0.0.0/8",
		"::/128",
	}
	for _, cidr := range raw {
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			panic("safenet: bad built-in CIDR " + cidr + ": " + err.Error())
		}
		blockedCIDRs = append(blockedCIDRs, p.Masked())
	}
}

// blockedHostnames is an exact-match deny list for well-known metadata endpoints.
var blockedHostnames = []string{
	"metadata.google.internal",
	"metadata.google.internal.",
}

// ValidatorConfig configures IP/hostname validation rules.
type ValidatorConfig struct {
	// AllowedPorts is the set of TCP ports that are permitted.
	// If nil, DefaultAllowedPorts is used.
	AllowedPorts []int

	// Allowlist is a set of hostnames or CIDRs that bypass the SSRF block.
	// Use sparingly – only for private infrastructure where the operator
	// deliberately allows intranet access.
	Allowlist []string

	// Denylist is a set of additional hostnames or CIDRs to block beyond
	// the built-in list.
	Denylist []string
}

// Validator validates IPs and hostnames against SSRF and DNS rebinding policy.
type Validator struct {
	allowedPorts map[int]bool
	allowCIDRs   []netip.Prefix
	allowHosts   map[string]bool
	denyCIDRs    []netip.Prefix
	denyHosts    map[string]bool
}

// NewValidator creates a Validator from the given config.
// A nil config uses defaults (ports 80 and 443, no allow/deny overrides).
func NewValidator(cfg *ValidatorConfig) (*Validator, error) {
	v := &Validator{
		allowedPorts: make(map[int]bool),
		allowHosts:   make(map[string]bool),
		denyHosts:    make(map[string]bool),
	}

	ports := DefaultAllowedPorts
	if cfg != nil && len(cfg.AllowedPorts) > 0 {
		ports = cfg.AllowedPorts
	}
	for _, p := range ports {
		v.allowedPorts[p] = true
	}

	if cfg != nil {
		for _, entry := range cfg.Allowlist {
			if p, err := netip.ParsePrefix(entry); err == nil {
				v.allowCIDRs = append(v.allowCIDRs, p.Masked())
			} else if ip, err2 := netip.ParseAddr(entry); err2 == nil {
				bits := 32
				if ip.Is6() {
					bits = 128
				}
				v.allowCIDRs = append(v.allowCIDRs, netip.PrefixFrom(ip, bits).Masked())
			} else {
				v.allowHosts[strings.ToLower(strings.TrimSuffix(entry, "."))] = true
			}
		}
		for _, entry := range cfg.Denylist {
			if p, err := netip.ParsePrefix(entry); err == nil {
				v.denyCIDRs = append(v.denyCIDRs, p.Masked())
			} else if ip, err2 := netip.ParseAddr(entry); err2 == nil {
				bits := 32
				if ip.Is6() {
					bits = 128
				}
				v.denyCIDRs = append(v.denyCIDRs, netip.PrefixFrom(ip, bits).Masked())
			} else {
				v.denyHosts[strings.ToLower(strings.TrimSuffix(entry, "."))] = true
			}
		}
	}

	return v, nil
}

// ValidatePort returns an error if port is not in the allowed set.
func (v *Validator) ValidatePort(port int) error {
	if !v.allowedPorts[port] {
		return &ErrBlockedPort{Port: port}
	}
	return nil
}

// ValidateHostname returns an error if the hostname itself (before DNS
// resolution) is on a known deny list (e.g. metadata.google.internal).
// Callers must still call ValidateIP after resolving.
func (v *Validator) ValidateHostname(hostname string) error {
	h := strings.ToLower(strings.TrimSuffix(hostname, "."))

	// Explicit allow overrides everything.
	if v.allowHosts[h] {
		return nil
	}

	// Explicit denylist.
	if v.denyHosts[h] {
		return &ErrBlockedDestination{Addr: hostname, Reason: "hostname on denylist"}
	}

	// Built-in blocked hostnames.
	for _, blocked := range blockedHostnames {
		if h == strings.ToLower(strings.TrimSuffix(blocked, ".")) {
			return &ErrBlockedDestination{Addr: hostname, Reason: "cloud metadata hostname"}
		}
	}

	return nil
}

// ValidateIP returns an error if addr is in a blocked network range.
// addr must be a pure IP address string (no port, no brackets).
func (v *Validator) ValidateIP(addr string) error {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		// Fall back to stdlib for addresses that ParseAddr can't handle.
		stdIP := net.ParseIP(addr)
		if stdIP == nil {
			return fmt.Errorf("safenet: cannot parse IP %q", addr)
		}
		if stdIP4 := stdIP.To4(); stdIP4 != nil {
			ip, _ = netip.AddrFromSlice(stdIP4)
		} else {
			ip, _ = netip.AddrFromSlice(stdIP)
		}
	}

	// Unmap IPv4-in-IPv6 (::ffff:a.b.c.d → a.b.c.d) so the IPv4 CIDRs hit.
	ip = ip.Unmap()

	// Explicit allowlist bypasses all blocks.
	for _, pfx := range v.allowCIDRs {
		if pfx.Contains(ip) {
			return nil
		}
	}

	// Extra denylist from config.
	for _, pfx := range v.denyCIDRs {
		if pfx.Contains(ip) {
			return &ErrBlockedDestination{Addr: addr, Reason: "address on denylist"}
		}
	}

	// Built-in blocked ranges.
	for _, pfx := range blockedCIDRs {
		if pfx.Contains(ip) {
			return &ErrBlockedDestination{Addr: addr, Reason: fmt.Sprintf("address in blocked range %s", pfx)}
		}
	}

	return nil
}

// ValidateAddrPort validates both the IP and port of a combined addr:port string.
func (v *Validator) ValidateAddrPort(addrPort string) error {
	host, portStr, err := net.SplitHostPort(addrPort)
	if err != nil {
		return fmt.Errorf("safenet: invalid addr:port %q: %w", addrPort, err)
	}
	port, err := net.LookupPort("tcp", portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("safenet: invalid port in %q", addrPort)
	}
	if err := v.ValidatePort(port); err != nil {
		return err
	}
	return v.ValidateIP(host)
}
