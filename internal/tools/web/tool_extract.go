package web

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"forcefield/internal/tools"
)

// ExtractTool implements the web_extract agent tool.
// It operates in two modes:
//   - URL mode: fetches the page via SafeNet and extracts from the HTML.
//   - HTML mode: extracts from a raw HTML string provided inline (zero network access).
type ExtractTool struct {
	safeGet   SafeGetter
	extractor Extractor
	scrapling Extractor
	limits    tools.Limits
}

// NewExtractTool constructs an ExtractTool.
func NewExtractTool(safeGet SafeGetter, native, scrapling Extractor) *ExtractTool {
	return &ExtractTool{safeGet: safeGet, extractor: native, scrapling: scrapling}
}

func (e *ExtractTool) SetLimits(l tools.Limits) { e.limits = l }
func (e *ExtractTool) ToolLimits() tools.Limits  { return e.resolveLimits() }

func (e *ExtractTool) resolveLimits() tools.Limits {
	return e.limits.WithDefaults(tools.DefaultLimitsFor("web_extract"))
}

func (*ExtractTool) Name() string { return "web_extract" }
func (*ExtractTool) Description() string {
	return "Extract structured content from a web page or raw HTML string into clean Markdown. " +
		"Supply either 'url' (fetched via safe HTTP client) or 'html' (inline raw HTML string; no network access). " +
		"Optionally specify extract_targets to focus on specific content types: article, headings, links, tables, metadata."
}

func (*ExtractTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "Absolute HTTP or HTTPS URL to fetch and extract. Mutually exclusive with 'html'.",
			},
			"html": map[string]any{
				"type":        "string",
				"description": "Raw HTML string to extract from directly. No network request is made. Mutually exclusive with 'url'.",
			},
			"base_url": map[string]any{
				"type":        "string",
				"description": "Base URL used to resolve relative links when processing raw HTML.",
			},
			"extract_targets": map[string]any{
				"type":        "array",
				"description": "Content types to extract: 'article', 'headings', 'links', 'tables', 'metadata'. Omit for all.",
				"items": map[string]any{
					"type": "string",
					"enum": []string{"article", "headings", "links", "tables", "metadata"},
				},
			},
			"backend": map[string]any{
				"type":        "string",
				"description": "Extraction backend: \"native\" (default) or \"scrapling\".",
				"enum":        []string{"native", "scrapling"},
			},
		},
	}
}

func (e *ExtractTool) metadataForURL() tools.Metadata {
	return tools.Metadata{
		Timeout:             tools.DefaultLimitsFor("web_extract").Timeout,
		SupportsParallel:    true,
		RequiredPermissions: []tools.Permission{tools.PermissionNetworkAccess},
	}
}

func (e *ExtractTool) Metadata() tools.Metadata {
	// Metadata must declare network permission conservatively because we
	// don't know at registration time whether url or html will be used.
	return e.metadataForURL()
}

func (e *ExtractTool) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	rawURL, _ := tools.OptionalStringArg(args, "url", "")
	rawHTML, _ := tools.OptionalStringArg(args, "html", "")
	baseURL, _ := tools.OptionalStringArg(args, "base_url", "")
	backend, _ := tools.OptionalStringArg(args, "backend", "native")

	if rawURL == "" && rawHTML == "" {
		return tools.Result{IsError: true, Content: "web_extract: provide either 'url' or 'html'"}, nil
	}
	if rawURL != "" && rawHTML != "" {
		return tools.Result{IsError: true, Content: "web_extract: 'url' and 'html' are mutually exclusive"}, nil
	}

	// Parse extract_targets.
	var targets []string
	if tv, ok := args["extract_targets"]; ok {
		if tArr, ok := tv.([]any); ok {
			for _, t := range tArr {
				if s, ok := t.(string); ok {
					targets = append(targets, s)
				}
			}
		}
	}

	lim := e.resolveLimits()
	tctx, cancel := context.WithTimeout(ctx, lim.Timeout)
	defer cancel()

	extractor := e.pickExtractor(backend)

	var htmlBody string
	var effectiveURL string

	if rawURL != "" {
		// URL mode: fetch via SafeNet.
		if err := validateExtractURL(rawURL); err != nil {
			return tools.Result{IsError: true, Content: "web_extract: " + err.Error()}, nil
		}
		maxBytes := tools.DefaultWebExtractMaxBytes
		body, finalURL, err := e.safeGet.SafeGet(tctx, rawURL, maxBytes)
		if err != nil {
			return tools.Result{IsError: true, Content: fmt.Sprintf("web_extract: fetch %s: %v", rawURL, err)}, nil
		}
		htmlBody = string(body)
		effectiveURL = finalURL
	} else {
		// HTML mode: use raw HTML directly — zero network access.
		htmlBody = rawHTML
		effectiveURL = baseURL
	}

	doc, err := extractor.ExtractHTML(tctx, htmlBody, effectiveURL, targets)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("web_extract: %v", err)}, nil
	}

	out := buildExtractOutput(doc, targets)
	return tools.Result{Content: out}, nil
}

func (e *ExtractTool) pickExtractor(backend string) Extractor {
	if backend == "scrapling" && e.scrapling != nil {
		return e.scrapling
	}
	if e.extractor != nil {
		return e.extractor
	}
	return &noopExtractor{}
}

func validateExtractURL(rawURL string) error {
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

func buildExtractOutput(doc Document, targets []string) string {
	wantAll := len(targets) == 0
	wants := make(map[string]bool, len(targets))
	for _, t := range targets {
		wants[t] = true
	}
	want := func(t string) bool { return wantAll || wants[t] }

	var sb strings.Builder
	sb.WriteString("<!-- BEGIN UNTRUSTED EXTERNAL CONTENT -->\n")

	if want("metadata") {
		if doc.Title != "" {
			fmt.Fprintf(&sb, "# %s\n\n", doc.Title)
		}
		if doc.Description != "" {
			fmt.Fprintf(&sb, "> %s\n\n", doc.Description)
		}
		parts := []string{}
		if doc.Author != "" {
			parts = append(parts, "Author: "+doc.Author)
		}
		if doc.Published != "" {
			parts = append(parts, "Published: "+doc.Published)
		}
		if doc.URL != "" {
			parts = append(parts, "URL: "+doc.URL)
		}
		if len(parts) > 0 {
			sb.WriteString(strings.Join(parts, " · ") + "\n\n")
		}
	}

	if want("article") && doc.Markdown != "" {
		sb.WriteString("## Article\n\n")
		sb.WriteString(doc.Markdown)
		sb.WriteString("\n\n")
	}

	if want("headings") && len(doc.Headings) > 0 {
		sb.WriteString("## Headings\n\n")
		for _, h := range doc.Headings {
			prefix := strings.Repeat("#", h.Level)
			fmt.Fprintf(&sb, "%s %s\n", prefix, h.Text)
		}
		sb.WriteString("\n")
	}

	if want("tables") && len(doc.Tables) > 0 {
		sb.WriteString("## Tables\n\n")
		for _, tbl := range doc.Tables {
			if len(tbl.Headers) > 0 {
				sb.WriteString("| " + strings.Join(tbl.Headers, " | ") + " |\n")
				sb.WriteString("|" + strings.Repeat(" --- |", len(tbl.Headers)) + "\n")
			}
			for _, row := range tbl.Rows {
				sb.WriteString("| " + strings.Join(row, " | ") + " |\n")
			}
			sb.WriteString("\n")
		}
	}

	if want("links") && len(doc.Links) > 0 {
		sb.WriteString("## Links\n\n")
		for _, l := range doc.Links {
			fmt.Fprintf(&sb, "- [%s](%s)\n", l.Text, l.URL)
		}
		sb.WriteString("\n")
	}

	if doc.Truncated {
		fmt.Fprintf(&sb, "\n[...content truncated at %d bytes]\n", doc.ByteCount)
	}
	sb.WriteString("<!-- END UNTRUSTED EXTERNAL CONTENT -->\n")
	return sb.String()
}
