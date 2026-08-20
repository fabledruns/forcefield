// Package permissions resolves tool execution decisions independently of UI
// and tool implementations.
package permissions

import "fmt"

// Decision is the outcome of a permission lookup for a single tool.
type Decision int

const (
	Allow Decision = iota
	Deny
	Ask
)

// String renders a Decision the way it's written in config.yaml.
func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Deny:
		return "deny"
	case Ask:
		return "ask"
	default:
		return fmt.Sprintf("decision(%d)", int(d))
	}
}

// ParseDecision parses the config.yaml spelling of a Decision ("allow",
// "deny", "ask"). An empty string is treated as "ask", the safest default:
// a tool nobody has expressed an opinion on should be confirmed, not
// silently allowed or silently denied.
func ParseDecision(s string) (Decision, error) {
	switch s {
	case "", "ask":
		return Ask, nil
	case "allow":
		return Allow, nil
	case "deny":
		return Deny, nil
	default:
		return Ask, fmt.Errorf("permissions: invalid decision %q (want \"allow\", \"deny\", or \"ask\")", s)
	}
}

// Rules contains the default decision and per-tool overrides.
type Rules struct {
	Default Decision
	Tools   map[string]Decision
}

// clone returns a deep copy of r so callers can hand it to a Store without
// that Store racing the Manager's own mutations of its map.
func (r Rules) clone() Rules {
	tools := make(map[string]Decision, len(r.Tools))
	for k, v := range r.Tools {
		tools[k] = v
	}
	return Rules{Default: r.Default, Tools: tools}
}
