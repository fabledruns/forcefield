package web

import (
	"context"
	"fmt"
	"strings"

	"forcefield/internal/tools"
)

// SearchTool implements the web_search agent tool.
type SearchTool struct {
	registry *SearchRegistry
	limits   tools.Limits
}

// NewSearchTool constructs a SearchTool backed by the given registry.
func NewSearchTool(reg *SearchRegistry) *SearchTool {
	return &SearchTool{registry: reg}
}

func (s *SearchTool) SetLimits(l tools.Limits) { s.limits = l }
func (s *SearchTool) ToolLimits() tools.Limits  { return s.resolveLimits() }

func (s *SearchTool) resolveLimits() tools.Limits {
	return s.limits.WithDefaults(tools.DefaultLimitsFor("web_search"))
}

func (*SearchTool) Name() string { return "web_search" }
func (*SearchTool) Description() string {
	return "Search the web for a query and return ranked results (titles, URLs, snippets). " +
		"Use for factual lookups, current events, library documentation, and research seed queries. " +
		"Results are summaries only; use web_fetch to retrieve full page content."
}

func (*SearchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "The search query string. Must not be empty.",
			},
			"provider": map[string]any{
				"type":        "string",
				"description": "Search provider name (e.g. \"duckduckgo\", \"brave\", \"tavily\", \"searxng\"). Omit for the default provider.",
			},
			"max_results": map[string]any{
				"type":        "number",
				"description": "Maximum number of results to return (1–20). Defaults to 10.",
			},
		},
		"required": []string{"query"},
	}
}

func (*SearchTool) Metadata() tools.Metadata {
	return tools.Metadata{
		Timeout:             tools.DefaultLimitsFor("web_search").Timeout,
		SupportsParallel:    true,
		RequiredPermissions: []tools.Permission{tools.PermissionNetworkAccess},
	}
}

// CheckBoundary validates that the query is non-empty (the network-access
// boundary is enforced by the scheduler; this checks argument validity only).
func (*SearchTool) CheckBoundary(args map[string]any) (string, error) {
	q, err := tools.StringArg(args, "query")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(q) == "" {
		return "", &tools.ArgumentError{Field: "query", Reason: "must not be empty"}
	}
	return "web_search:" + q, nil
}

func (s *SearchTool) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	query, err := tools.StringArg(args, "query")
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}
	if strings.TrimSpace(query) == "" {
		return tools.Result{IsError: true, Content: "web_search: query must not be empty"}, nil
	}

	providerName, _ := tools.OptionalStringArg(args, "provider", "")
	maxResults := 10
	if v, ok := args["max_results"]; ok {
		switch n := v.(type) {
		case float64:
			maxResults = int(n)
		case int:
			maxResults = n
		case int64:
			maxResults = int(n)
		}
	}
	if maxResults < 1 {
		maxResults = 1
	}
	if maxResults > 20 {
		maxResults = 20
	}

	lim := s.resolveLimits()
	tctx, cancel := context.WithTimeout(ctx, lim.Timeout)
	defer cancel()

	var provider SearchProvider
	var provErr error
	if providerName != "" {
		provider, provErr = s.registry.Lookup(providerName)
		if provErr != nil {
			return tools.Result{IsError: true, Content: fmt.Sprintf("web_search: unknown provider %q", providerName)}, nil
		}
	} else {
		provider, provErr = s.registry.Default()
		if provErr != nil {
			return tools.Result{IsError: true, Content: "web_search: no search provider available"}, nil
		}
	}

	result, err := provider.Search(tctx, SearchQuery{Query: query, MaxResults: maxResults})
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("web_search: %v", err)}, nil
	}

	if len(result.Items) == 0 {
		return tools.Result{Content: fmt.Sprintf("web_search: no results found for %q", query)}, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Search results for %q (provider: %s, %d results):\n\n", query, result.Provider, len(result.Items))
	for _, item := range result.Items {
		fmt.Fprintf(&sb, "%d. **%s**\n   URL: %s\n   %s\n\n", item.Rank, item.Title, item.URL, item.Snippet)
	}

	out := sb.String()
	out, truncated := tools.CapLines(out, lim.MaxLines)
	if truncated {
		out += fmt.Sprintf("\n[...results truncated at %d lines]\n", lim.MaxLines)
	}
	return tools.Result{Content: out}, nil
}
