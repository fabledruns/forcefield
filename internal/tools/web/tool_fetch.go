package web

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"forcefield/internal/tools"
)

// FetchTool implements the web_fetch agent tool.
type FetchTool struct {
	safeGet   SafeGetter
	extractor Extractor
	scrapling Extractor
	limits    tools.Limits
}

// SafeGetter is the minimal interface FetchTool needs from safenet.Client.
type SafeGetter interface {
	SafeGet(ctx context.Context, rawURL string, maxBytes int) ([]byte, string, error)
}

// NewFetchTool constructs a FetchTool. safeGet performs SSRF-safe HTTP
// retrieval; native and scrapling are the extraction backends.
func NewFetchTool(safeGet SafeGetter, native, scrapling Extractor) *FetchTool {
	return &FetchTool{safeGet: safeGet, extractor: native, scrapling: scrapling}
}

func (f *FetchTool) SetLimits(l tools.Limits) { f.limits = l }
func (f *FetchTool) ToolLimits() tools.Limits  { return f.resolveLimits() }

func (f *FetchTool) resolveLimits() tools.Limits {
	return f.limits.WithDefaults(tools.DefaultLimitsFor("web_fetch"))
}

func (*FetchTool) Name() string { return "web_fetch" }
func (*FetchTool) Description() string {
	return "Fetch a web page via a safe HTTP client and return its content as clean Markdown. " +
		"The page is retrieved through an SSRF-safe client that blocks private network access, " +
		"and the HTML is converted to Markdown with boilerplate removed. " +
		"Use web_search first to discover URLs."
}

func (*FetchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "Absolute HTTP or HTTPS URL to fetch.",
			},
			"max_bytes": map[string]any{
				"type":        "number",
				"description": "Maximum response bytes to read (default 51200; max 1048576).",
			},
			"backend": map[string]any{
				"type":        "string",
				"description": "Extraction backend: \"native\" (default) or \"scrapling\" (requires Python + scrapling).",
				"enum":        []string{"native", "scrapling"},
			},
		},
		"required": []string{"url"},
	}
}

func (*FetchTool) Metadata() tools.Metadata {
	return tools.Metadata{
		Timeout:             tools.DefaultLimitsFor("web_fetch").Timeout,
		SupportsParallel:    true,
		RequiredPermissions: []tools.Permission{tools.PermissionNetworkAccess},
	}
}

// CheckBoundary validates URL syntax before the scheduler prompts for
// permission. It does not make any network request.
func (*FetchTool) CheckBoundary(args map[string]any) (string, error) {
	rawURL, err := tools.StringArg(args, "url")
	if err != nil {
		return "", err
	}
	if err := validateFetchURL(rawURL); err != nil {
		return "", err
	}
	return rawURL, nil
}

func (f *FetchTool) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	rawURL, err := tools.StringArg(args, "url")
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}
	if err := validateFetchURL(rawURL); err != nil {
		return tools.Result{IsError: true, Content: "web_fetch: " + err.Error()}, nil
	}

	lim := f.resolveLimits()
	maxBytes := lim.MaxBytes
	if maxBytes <= 0 {
		maxBytes = tools.DefaultWebFetchMaxBytes
	}
	if v, ok := args["max_bytes"]; ok {
		switch n := v.(type) {
		case float64:
			maxBytes = int(n)
		case int:
			maxBytes = n
		}
		if maxBytes > 1<<20 {
			maxBytes = 1 << 20
		}
		if maxBytes < 1 {
			maxBytes = tools.DefaultWebFetchMaxBytes
		}
	}

	backend, _ := tools.OptionalStringArg(args, "backend", "native")

	tctx, cancel := context.WithTimeout(ctx, lim.Timeout)
	defer cancel()

	// Fetch via SafeNet.
	htmlBytes, finalURL, err := f.safeGet.SafeGet(tctx, rawURL, maxBytes)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("web_fetch: fetch %s: %v", rawURL, err)}, nil
	}

	extractor := f.pickExtractor(backend)
	doc, err := extractor.ExtractHTML(tctx, string(htmlBytes), finalURL, nil)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("web_fetch: extract %s: %v", rawURL, err)}, nil
	}

	content := buildFetchOutput(doc, rawURL)
	return tools.Result{Content: content}, nil
}

func (f *FetchTool) pickExtractor(backend string) Extractor {
	if backend == "scrapling" && f.scrapling != nil {
		return f.scrapling
	}
	if f.extractor != nil {
		return f.extractor
	}
	return &noopExtractor{}
}

func validateFetchURL(rawURL string) error {
	if strings.TrimSpace(rawURL) == "" {
		return &tools.ArgumentError{Field: "url", Reason: "must not be empty"}
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return &tools.ArgumentError{Field: "url", Reason: "invalid URL: " + err.Error()}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return &tools.ArgumentError{Field: "url", Reason: "must be an http or https URL"}
	}
	if u.Host == "" {
		return &tools.ArgumentError{Field: "url", Reason: "missing host"}
	}
	return nil
}

func buildFetchOutput(doc Document, originalURL string) string {
	var sb strings.Builder

	// Untrusted-content fence: clearly mark this is external data.
	sb.WriteString("<!-- BEGIN UNTRUSTED EXTERNAL CONTENT -->\n")

	if doc.Title != "" {
		fmt.Fprintf(&sb, "# %s\n\n", doc.Title)
	}
	if doc.Description != "" {
		fmt.Fprintf(&sb, "> %s\n\n", doc.Description)
	}
	meta := []string{}
	if doc.Author != "" {
		meta = append(meta, "Author: "+doc.Author)
	}
	if doc.Published != "" {
		meta = append(meta, "Published: "+doc.Published)
	}
	dispURL := doc.URL
	if dispURL == "" {
		dispURL = originalURL
	}
	meta = append(meta, "URL: "+dispURL)
	if doc.CanonicalURL != "" && doc.CanonicalURL != dispURL {
		meta = append(meta, "Canonical: "+doc.CanonicalURL)
	}
	sb.WriteString(strings.Join(meta, " · ") + "\n\n---\n\n")

	if doc.Markdown != "" {
		sb.WriteString(doc.Markdown)
	} else if doc.RawText != "" {
		sb.WriteString(doc.RawText)
	}

	if doc.Truncated {
		fmt.Fprintf(&sb, "\n\n[...content truncated at %d bytes]\n", doc.ByteCount)
	}
	sb.WriteString("\n<!-- END UNTRUSTED EXTERNAL CONTENT -->\n")
	return sb.String()
}

// noopExtractor is a fallback that returns an empty document when no backend is configured.
type noopExtractor struct{}

func (*noopExtractor) Name() string { return "noop" }
func (*noopExtractor) ExtractHTML(_ context.Context, _ string, baseURL string, _ []string) (Document, error) {
	return Document{URL: baseURL, Backend: "noop"}, nil
}
