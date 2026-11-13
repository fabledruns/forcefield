package web

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"forcefield/internal/tools"
)

// CrawlTool implements the web_crawl agent tool.
// It performs a polite breadth-first crawl from a start URL with
// robots.txt enforcement, per-domain rate limiting, and deduplication.
type CrawlTool struct {
	safeGet   SafeGetter
	extractor Extractor
	scrapling Extractor
	robots    *RobotsChecker
	ratelimit *RateLimiter
	limits    tools.Limits
}

// NewCrawlTool constructs a CrawlTool. robots and ratelimit may be nil;
// nil implementations skip enforcement (safe for tests or offline mode).
func NewCrawlTool(safeGet SafeGetter, native, scrapling Extractor, robots *RobotsChecker, rl *RateLimiter) *CrawlTool {
	return &CrawlTool{
		safeGet:   safeGet,
		extractor: native,
		scrapling: scrapling,
		robots:    robots,
		ratelimit: rl,
	}
}

func (c *CrawlTool) SetLimits(l tools.Limits) { c.limits = l }
func (c *CrawlTool) ToolLimits() tools.Limits { return c.resolveLimits() }

func (c *CrawlTool) resolveLimits() tools.Limits {
	return c.limits.WithDefaults(tools.DefaultLimitsFor("web_crawl"))
}

func (*CrawlTool) Name() string { return "web_crawl" }
func (*CrawlTool) Description() string {
	return "Crawl a website starting from a given URL, following links up to a configurable depth. " +
		"Respects robots.txt and applies polite rate limiting. " +
		"Returns a structured summary of discovered pages with titles and snippets."
}

func (*CrawlTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"start_url": map[string]any{
				"type":        "string",
				"description": "The absolute HTTP or HTTPS URL to start crawling from.",
			},
			"max_pages": map[string]any{
				"type":        "number",
				"description": "Maximum pages to crawl (1–20, default 10).",
			},
			"max_depth": map[string]any{
				"type":        "number",
				"description": "Maximum link-following depth from the start URL (1–3, default 2).",
			},
			"allowed_domains": map[string]any{
				"type":        "array",
				"description": "If set, only follow links to these domains (exact match or subdomain). Defaults to the start URL's domain.",
				"items": map[string]any{
					"type": "string",
				},
			},
			"backend": map[string]any{
				"type":        "string",
				"description": "Extraction backend: \"native\" (default) or \"scrapling\".",
				"enum":        []string{"native", "scrapling"},
			},
		},
		"required": []string{"start_url"},
	}
}

func (*CrawlTool) Metadata() tools.Metadata {
	return tools.Metadata{
		Timeout:             tools.DefaultLimitsFor("web_crawl").Timeout,
		SupportsParallel:    false, // crawls are sequential to be polite
		RequiredPermissions: []tools.Permission{tools.PermissionNetworkAccess},
	}
}

type crawledPage struct {
	URL     string
	Title   string
	Snippet string
	Links   int
	Depth   int
}

type crawlQueueItem struct {
	url   string
	depth int
}

func (c *CrawlTool) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	startURL, err := tools.StringArg(args, "start_url")
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}
	if err := validateFetchURL(startURL); err != nil {
		return tools.Result{IsError: true, Content: "web_crawl: " + err.Error()}, nil
	}

	maxPages := 10
	if v, ok := args["max_pages"]; ok {
		if n, ok := v.(float64); ok {
			maxPages = int(n)
		}
	}
	if maxPages < 1 {
		maxPages = 1
	}
	if maxPages > 20 {
		maxPages = 20
	}

	maxDepth := 2
	if v, ok := args["max_depth"]; ok {
		if n, ok := v.(float64); ok {
			maxDepth = int(n)
		}
	}
	if maxDepth < 1 {
		maxDepth = 1
	}
	if maxDepth > 3 {
		maxDepth = 3
	}

	var allowedDomains []string
	if v, ok := args["allowed_domains"]; ok {
		if arr, ok := v.([]any); ok {
			for _, d := range arr {
				if s, ok := d.(string); ok && s != "" {
					allowedDomains = append(allowedDomains, strings.ToLower(s))
				}
			}
		}
	}
	// Default: restrict to the start URL's domain.
	if len(allowedDomains) == 0 {
		if u, parseErr := url.Parse(startURL); parseErr == nil {
			allowedDomains = []string{u.Hostname()}
		}
	}

	backend, _ := tools.OptionalStringArg(args, "backend", "native")
	extractor := c.pickExtractor(backend)

	lim := c.resolveLimits()
	tctx, cancel := context.WithTimeout(ctx, lim.Timeout)
	defer cancel()

	visited := make(map[string]bool)
	var mu sync.Mutex
	var pages []crawledPage

	queue := []crawlQueueItem{{url: startURL, depth: 0}}
	maxFetchBytes := tools.DefaultWebFetchMaxBytes

	for len(queue) > 0 && len(pages) < maxPages {
		if err := tctx.Err(); err != nil {
			break
		}

		item := queue[0]
		queue = queue[1:]

		normURL := normalizeURLForDedup(item.url)
		mu.Lock()
		already := visited[normURL]
		if !already {
			visited[normURL] = true
		}
		mu.Unlock()
		if already {
			continue
		}

		// Robots check.
		if c.robots != nil {
			allowed, err := c.robots.IsAllowed(item.url)
			if err == nil && !allowed {
				continue
			}
		}

		// Rate limit.
		if c.ratelimit != nil {
			if u, parseErr := url.Parse(item.url); parseErr == nil {
				_ = c.ratelimit.Wait(tctx, u.Hostname())
			}
		}

		htmlBytes, finalURL, fetchErr := c.safeGet.SafeGet(tctx, item.url, maxFetchBytes)
		if fetchErr != nil {
			// Skip failed pages, don't abort the whole crawl.
			continue
		}

		doc, extractErr := extractor.ExtractHTML(tctx, string(htmlBytes), finalURL, []string{"article", "headings", "links", "metadata"})
		if extractErr != nil {
			continue
		}

		snippet := buildCrawlSnippet(doc.Markdown, 200)
		pages = append(pages, crawledPage{
			URL:     finalURL,
			Title:   doc.Title,
			Snippet: snippet,
			Links:   len(doc.Links),
			Depth:   item.depth,
		})

		// Enqueue links for the next depth level.
		if item.depth < maxDepth {
			for _, link := range doc.Links {
				if !isDomainAllowed(link.URL, allowedDomains) {
					continue
				}
				normLink := normalizeURLForDedup(link.URL)
				mu.Lock()
				seen := visited[normLink]
				mu.Unlock()
				if !seen {
					queue = append(queue, crawlQueueItem{url: link.URL, depth: item.depth + 1})
				}
			}
		}
	}

	out := formatCrawlOutput(startURL, pages, maxPages, maxDepth)
	return tools.Result{Content: out}, nil
}

func (c *CrawlTool) pickExtractor(backend string) Extractor {
	if backend == "scrapling" && c.scrapling != nil {
		return c.scrapling
	}
	if c.extractor != nil {
		return c.extractor
	}
	return &noopExtractor{}
}

func buildCrawlSnippet(markdown string, maxRunes int) string {
	text := strings.TrimSpace(markdown)
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		stripped := strings.TrimLeft(line, "# ")
		stripped = strings.TrimSpace(stripped)
		if stripped != "" {
			lines = append(lines, stripped)
		}
	}
	joined := strings.Join(lines, " ")
	runes := []rune(joined)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return joined
}

func normalizeURLForDedup(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.Fragment = ""
	return strings.ToLower(u.String())
}

func isDomainAllowed(rawURL string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, a := range allowed {
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}

func formatCrawlOutput(startURL string, pages []crawledPage, maxPages, maxDepth int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Crawl Report\n\nStart URL: %s\nPages crawled: %d (cap: %d, depth cap: %d)\n\n",
		startURL, len(pages), maxPages, maxDepth)
	for i, p := range pages {
		title := p.Title
		if title == "" {
			title = "(no title)"
		}
		fmt.Fprintf(&sb, "## %d. %s\n", i+1, title)
		fmt.Fprintf(&sb, "URL: %s (depth %d)\n", p.URL, p.Depth)
		if p.Snippet != "" {
			fmt.Fprintf(&sb, "\n%s\n", p.Snippet)
		}
		fmt.Fprintf(&sb, "Outgoing links found: %d\n\n", p.Links)
	}
	return sb.String()
}
