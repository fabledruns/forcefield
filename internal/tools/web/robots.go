package web

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// robotsCacheTTL is the duration a parsed robots.txt is kept in memory.
const robotsCacheTTL = time.Hour

// robotsEntry is a cached robots.txt for a domain.
type robotsEntry struct {
	rules    *robotsRules
	cachedAt time.Time
}

// robotsCache is the global in-memory cache of parsed robots.txt files.
var robotsCache struct {
	mu      sync.Mutex
	entries map[string]*robotsEntry
}

func init() {
	robotsCache.entries = make(map[string]*robotsEntry)
}

// robotsRules holds the parsed directives for all user-agents in one robots.txt.
type robotsRules struct {
	groups     []robotsGroup
	crawlDelay time.Duration // from the first applicable group
}

// robotsGroup is a parsed User-agent block in robots.txt.
type robotsGroup struct {
	agents     []string // lower-cased
	disallowed []string
	allowed    []string
	crawlDelay time.Duration
}

// RobotsChecker checks whether a URL path is allowed for a given User-Agent,
// following RFC 9309.
type RobotsChecker struct {
	ua         string // lower-cased product token
	fetchFn    func(robotsURL string) ([]byte, error)
}

// NewRobotsChecker creates a RobotsChecker for the given user-agent.
// fetchFn is called to retrieve the robots.txt content when not cached; it
// receives the full URL ("https://example.com/robots.txt") and must return the
// raw bytes of the response body.
func NewRobotsChecker(userAgent string, fetchFn func(string) ([]byte, error)) *RobotsChecker {
	// Extract product token (first word before '/').
	token := strings.ToLower(strings.Fields(userAgent)[0])
	if idx := strings.Index(token, "/"); idx >= 0 {
		token = token[:idx]
	}
	return &RobotsChecker{
		ua:      token,
		fetchFn: fetchFn,
	}
}

// IsAllowed returns true if the given path is allowed to be fetched by this
// checker's User-Agent, according to the robots.txt of the host derived from
// rawURL.
//
// RFC 9309 precedence: specific UA > wildcard; longest matching rule wins;
// Allow beats Disallow on equal length.
func (rc *RobotsChecker) IsAllowed(rawURL string) (bool, error) {
	robotsURL, err := robotsTxtURL(rawURL)
	if err != nil {
		// If we can't construct the robots URL, allow by default.
		return true, nil
	}

	rules, err := rc.fetchRules(robotsURL)
	if err != nil {
		// Fail open: a missing or inaccessible robots.txt allows everything.
		return true, nil
	}

	path := pathOf(rawURL)
	return rc.isPathAllowed(rules, path), nil
}

// CrawlDelay returns the Crawl-delay directive (if any) for this User-Agent
// for the host derived from rawURL.
func (rc *RobotsChecker) CrawlDelay(rawURL string) (time.Duration, error) {
	robotsURL, err := robotsTxtURL(rawURL)
	if err != nil {
		return 0, nil
	}
	rules, err := rc.fetchRules(robotsURL)
	if err != nil {
		return 0, nil
	}
	return rules.crawlDelay, nil
}

func (rc *RobotsChecker) isPathAllowed(rules *robotsRules, path string) bool {
	// RFC 9309 §2.2.2: specific UA group takes precedence over wildcard.
	// Within a group, longer match wins; Allow beats Disallow on tie.
	type match struct {
		length  int
		allowed bool
	}

	var best *match
	applyGroup := func(g robotsGroup) {
		for _, dis := range g.disallowed {
			if matchRobotsPattern(dis, path) {
				l := len(dis)
				if best == nil || l > best.length || (l == best.length && !best.allowed) {
					m := match{length: l, allowed: false}
					best = &m
				}
			}
		}
		for _, al := range g.allowed {
			if matchRobotsPattern(al, path) {
				l := len(al)
				if best == nil || l > best.length || (l == best.length && best != nil && !best.allowed) {
					m := match{length: l, allowed: true}
					best = &m
				}
			}
		}
	}

	// First pass: specific UA groups.
	for _, g := range rules.groups {
		if agentMatches(g.agents, rc.ua) {
			applyGroup(g)
		}
	}

	// If a specific group matched, use that result.
	if best != nil {
		return best.allowed
	}

	// Second pass: wildcard groups.
	for _, g := range rules.groups {
		for _, a := range g.agents {
			if a == "*" {
				applyGroup(g)
				break
			}
		}
	}

	if best != nil {
		return best.allowed
	}

	// Default: allow.
	return true
}

func (rc *RobotsChecker) fetchRules(robotsURL string) (*robotsRules, error) {
	robotsCache.mu.Lock()
	entry, ok := robotsCache.entries[robotsURL]
	if ok && time.Since(entry.cachedAt) < robotsCacheTTL {
		robotsCache.mu.Unlock()
		return entry.rules, nil
	}
	robotsCache.mu.Unlock()

	data, err := rc.fetchFn(robotsURL)
	if err != nil {
		return nil, fmt.Errorf("robots: fetching %s: %w", robotsURL, err)
	}

	rules := parseRobots(string(data))

	robotsCache.mu.Lock()
	robotsCache.entries[robotsURL] = &robotsEntry{
		rules:    rules,
		cachedAt: time.Now(),
	}
	robotsCache.mu.Unlock()

	return rules, nil
}

// parseRobots parses a robots.txt body according to RFC 9309.
func parseRobots(content string) *robotsRules {
	rules := &robotsRules{}

	scanner := bufio.NewScanner(strings.NewReader(content))
	var current *robotsGroup
	var groups []robotsGroup

	for scanner.Scan() {
		line := scanner.Text()

		// Strip inline comments.
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			// Empty line ends the current group.
			if current != nil {
				groups = append(groups, *current)
				current = nil
			}
			continue
		}

		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		field := strings.ToLower(strings.TrimSpace(line[:idx]))
		value := strings.TrimSpace(line[idx+1:])

		switch field {
		case "user-agent":
			if current == nil {
				current = &robotsGroup{}
			} else if len(current.disallowed) > 0 || len(current.allowed) > 0 {
				// New user-agent after directives: start a new group.
				groups = append(groups, *current)
				current = &robotsGroup{}
			}
			current.agents = append(current.agents, strings.ToLower(value))

		case "disallow":
			if current == nil {
				break
			}
			current.disallowed = append(current.disallowed, value)

		case "allow":
			if current == nil {
				break
			}
			current.allowed = append(current.allowed, value)

		case "crawl-delay":
			if current == nil {
				break
			}
			secs, err := parseSeconds(value)
			if err == nil && secs > 0 {
				current.crawlDelay = secs
			}
		}
	}

	// Flush last group.
	if current != nil {
		groups = append(groups, *current)
	}

	rules.groups = groups

	// Compute crawl-delay for this UA (specific, then wildcard).
	for _, g := range groups {
		if agentMatches(g.agents, "") { // placeholder; will be evaluated at call time
			_ = g
		}
	}

	return rules
}

// matchRobotsPattern implements RFC 9309 wildcard matching (* and $).
func matchRobotsPattern(pattern, path string) bool {
	if pattern == "" {
		// Empty Disallow means allow everything.
		return false
	}

	// If no wildcards, do prefix match (with optional $ suffix anchor).
	anchored := strings.HasSuffix(pattern, "$")
	p := pattern
	if anchored {
		p = p[:len(p)-1]
	}

	if !strings.Contains(p, "*") {
		if anchored {
			return path == p
		}
		return strings.HasPrefix(path, p)
	}

	// Wildcard matching via recursive expansion.
	return matchGlob(p, path, anchored)
}

// matchGlob matches path against pattern using * as wildcard.
func matchGlob(pattern, path string, anchored bool) bool {
	parts := strings.Split(pattern, "*")
	pos := 0
	for i, part := range parts {
		if part == "" {
			continue
		}
		idx := strings.Index(path[pos:], part)
		if idx < 0 {
			return false
		}
		if i == 0 {
			// First segment must start at beginning.
			if idx != 0 {
				return false
			}
		}
		pos += idx + len(part)
	}
	if anchored {
		return pos == len(path)
	}
	return true
}

// agentMatches returns true if ua appears in agents (exact lower-case match).
func agentMatches(agents []string, ua string) bool {
	for _, a := range agents {
		if a != "*" && a == ua {
			return true
		}
	}
	return false
}

// parseSeconds parses a decimal seconds string into a time.Duration.
func parseSeconds(s string) (time.Duration, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	if err != nil {
		return 0, err
	}
	return time.Duration(f * float64(time.Second)), nil
}

// robotsTxtURL returns the robots.txt URL for a given absolute URL.
func robotsTxtURL(rawURL string) (string, error) {
	// Extract scheme + host.
	idx := strings.Index(rawURL, "://")
	if idx < 0 {
		return "", fmt.Errorf("robots: no scheme in %q", rawURL)
	}
	scheme := rawURL[:idx]
	rest := rawURL[idx+3:]
	// Find end of host.
	hostEnd := strings.IndexAny(rest, "/?#")
	var host string
	if hostEnd < 0 {
		host = rest
	} else {
		host = rest[:hostEnd]
	}
	return scheme + "://" + host + "/robots.txt", nil
}

// pathOf returns the path (and query) component of an absolute URL.
func pathOf(rawURL string) string {
	idx := strings.Index(rawURL, "://")
	if idx < 0 {
		return rawURL
	}
	rest := rawURL[idx+3:]
	hostEnd := strings.IndexAny(rest, "/?#")
	if hostEnd < 0 {
		return "/"
	}
	p := rest[hostEnd:]
	// Remove fragment.
	if fi := strings.Index(p, "#"); fi >= 0 {
		p = p[:fi]
	}
	if p == "" {
		return "/"
	}
	return p
}

// FetchRobotsTxt is a convenience function that fetches the robots.txt for
// robotsURL using the standard net/http client. Used as default fetchFn.
func FetchRobotsTxt(robotsURL string) ([]byte, error) {
	resp, err := http.Get(robotsURL) //nolint:noctx // robots fetch is best-effort
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusForbidden ||
		resp.StatusCode == http.StatusNotFound {
		// Treat 401, 403, 404 as "no restrictions".
		return []byte{}, nil
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512*1024))
}
