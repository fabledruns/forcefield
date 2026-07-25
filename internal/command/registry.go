package command

import (
	"fmt"
	"sort"
)

// Registry holds every registered command, indexed for O(1) lookup by
// canonical name or alias. It is built once at startup (see
// tui.newRegistry) and treated as read-only for the lifetime of the
// session — there's no need for locking or dynamic
// registration/unregistration.
type Registry struct {
	byName map[string]Command 
	unique []Command        
	names  []string           
}

// NewRegistry returns an empty Registry ready for Register calls.
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]Command)}
}

// Register adds cmd under its canonical name and all of its aliases, so
// aliases resolve to the exact same Command instance rather than a
// duplicate. Register panics on a name collision: that's a programming
// error in how commands are wired up, and it's far better to crash
// immediately at startup than to silently shadow a command at runtime.
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

// Lookup finds the command registered under name, which may be a
// canonical name or an alias. It's a single map access, safe to call on
// every submitted line without any noticeable cost.
func (r *Registry) Lookup(name string) (Command, bool) {
	cmd, ok := r.byName[name]
	return cmd, ok
}

// All returns every registered command exactly once (aliases excluded),
// in registration order. It backs /help's command listing.
func (r *Registry) All() []Command {
	return r.unique
}

// Suggest returns up to n known command names closest to input by edit
// distance, closest first. It's only ever called after a failed Lookup,
// so its O(len(names)) scan never runs on the hot path of dispatching a
// recognized command.
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
