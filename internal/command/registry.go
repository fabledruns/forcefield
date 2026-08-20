package command

import (
	"fmt"
	"sort"
	"strings"
)

// Registry stores commands by name and alias.
type Registry struct {
	byName map[string]Command
	unique []Command
	names  []string
}

// NewRegistry returns an empty Registry ready for Register calls.
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]Command)}
}

// Register adds cmd under its canonical name and aliases. It panics on
// name collisions because duplicate command registration is a wiring error.
func (r *Registry) Register(cmd Command) {
	names := make([]string, 0, 1+len(cmd.Aliases()))
	names = append(names, cmd.Name())
	names = append(names, cmd.Aliases()...)

	for _, name := range names {
		if existing, exists := r.byName[name]; exists {
			panic(fmt.Sprintf(
				"command: %q already registered (as %q)", name, existing.Name(),
			))
		}
	}

	for _, name := range names {
		r.byName[name] = cmd
		r.names = append(r.names, name)
	}
	r.unique = append(r.unique, cmd)
}

// Lookup finds a command by name or alias.
func (r *Registry) Lookup(name string) (Command, bool) {
	cmd, ok := r.byName[name]
	return cmd, ok
}

// All returns registered commands in registration order.
func (r *Registry) All() []Command {
	return r.unique
}

// Match returns canonical commands whose names have prefix, sorted by name.
func (r *Registry) Match(prefix string) []Command {
	matches := make([]Command, 0, 4)
	for _, cmd := range r.unique {
		if strings.HasPrefix(cmd.Name(), prefix) {
			matches = append(matches, cmd)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Name() < matches[j].Name()
	})
	return matches
}

// Suggest returns up to n command names closest to input.
func (r *Registry) Suggest(input string, n int) []string {
	type scored struct {
		name string
		dist int
	}

	candidates := make([]scored, 0, len(r.names))
	for _, name := range r.names {
		candidates = append(candidates, scored{name, levenshtein(input, name)})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].dist != candidates[j].dist {
			return candidates[i].dist < candidates[j].dist
		}
		return candidates[i].name < candidates[j].name // deterministic tie-break
	})

	// A large edit distance means the input probably isn't a typo of
	// anything we know, so a "did you mean" for it would just be noise.
	const maxDistance = 3

	out := make([]string, 0, n)
	for _, c := range candidates {
		if c.dist > maxDistance || len(out) == n {
			break
		}
		out = append(out, c.name)
	}
	return out
}
