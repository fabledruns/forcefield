package builtin

import (
	"fmt"
	"strings"

	"forcefield/internal/command"
	"forcefield/internal/skills"
)

// Skills is the /skills command. It lists the global skill catalog and
// displays individual skill bodies. Skills are global-only
// (~/.forcefield/skills/) and filesystem-first.
type Skills struct{}

// NewSkills returns a ready-to-register /skills command.
func NewSkills() *Skills { return &Skills{} }

func (Skills) Name() string        { return "skills" }
func (Skills) Aliases() []string   { return []string{"skill"} }
func (Skills) Description() string { return "List and inspect available skills." }
func (Skills) Usage() string       { return "/skills [list|show <id>]" }

func (s *Skills) Execute(ctx command.Context, args []string) error {
	if len(args) == 0 {
		return s.list(ctx)
	}

	switch strings.ToLower(args[0]) {
	case "list":
		if len(args) != 1 {
			return fmt.Errorf("usage: %s", s.Usage())
		}
		return s.list(ctx)
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("usage: /skills show <id>")
		}
		if len(args) > 2 {
			// Join remaining tokens with a space to allow names with spaces,
			// e.g. "/skills show Go Style Guide" -> "Go Style Guide".
			// The id is normalized before lookup.
			id := strings.Join(args[1:], " ")
			return s.show(ctx, id)
		}
		return s.show(ctx, args[1])
	case "help", "?":
		ctx.Println("Usage: %s", s.Usage())
		ctx.Println("  /skills        — list available skills")
		ctx.Println("  /skills list   — list available skills")
		ctx.Println("  /skills show <id> — display one skill's full instructions")
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q — usage: %s", args[0], s.Usage())
	}
}

func (s *Skills) list(ctx command.Context) error {
	catalog := ctx.Skills()
	if len(catalog) == 0 {
		ctx.Println("No skills found.")
		ctx.Println("Add Markdown files to ~/.forcefield/skills/ (e.g. ~/.forcefield/skills/review.md or ~/.forcefield/skills/git-review/SKILL.md) and restart Forcefield.")
		return nil
	}

	// Catalog is already sorted by discovery order; ensure display is
	// sorted by ID for stability.
	// ctx.Skills should already be sorted, but re-sort defensively.
	// We avoid importing sort to keep this small; the store guarantees order.
	ctx.Println("Available skills (%d):", len(catalog))
	for _, sk := range catalog {
		if sk.Description != "" {
			ctx.Println("  - id: `%s`, name: %q — %s", sk.ID, sk.Name, sk.Description)
		} else {
			ctx.Println("  - id: `%s`, name: %q", sk.ID, sk.Name)
		}
	}
	ctx.Println("")
	ctx.Println("Use /skills show <id> to view a skill's full instructions.")
	ctx.Println("Skills are loaded on demand via the load_skill tool when the model needs them.")
	return nil
}

func (s *Skills) show(ctx command.Context, rawID string) error {
	rawID = strings.TrimSpace(rawID)
	if rawID == "" {
		return fmt.Errorf("usage: /skills show <id>")
	}

	// Try the id as-given, then lower-cased, then normalized. This makes
	// "/skills show Go Style Guide" and "/skills show go-style-guide"
	// equivalent without forcing the user to know the exact kebab form.
	candidates := []string{rawID, strings.ToLower(rawID), skills.NormalizeID(rawID)}
	// Also try the normalized lower form as fallback for ids with
	// non-alphanumeric characters.
	var body string
	var found string
	var lastErr error
	for _, id := range candidates {
		if id == "" {
			continue
		}
		b, err := ctx.LoadSkill(id)
		if err == nil {
			body = b
			found = id
			lastErr = nil
			break
		}
		lastErr = err
		// Only retry on not-found; other errors (e.g. unreadable file) are returned.
		if !isNotFound(err) {
			return err
		}
	}
	if lastErr != nil && body == "" {
		// Not found: list available ids to help the user.
		catalog := ctx.Skills()
		if len(catalog) == 0 {
			return fmt.Errorf("skill %q not found — no skills are installed", rawID)
		}
		ids := make([]string, 0, len(catalog))
		for _, sk := range catalog {
			ids = append(ids, sk.ID)
		}
		// Show up to 5 ids.
		limit := 5
		if len(ids) < limit {
			limit = len(ids)
		}
		return fmt.Errorf("skill %q not found — available: %s", rawID, strings.Join(ids[:limit], ", "))
	}

	// Lookup display name for header.
	catalog := ctx.Skills()
	displayName := found
	displayDesc := ""
	for _, sk := range catalog {
		if sk.ID == found {
			displayName = sk.Name
			displayDesc = sk.Description
			break
		}
	}

	// Header mirrors FormatCatalog context but for a single skill.
	if displayDesc != "" {
		ctx.Println("Skill `%s` — %q — %s\n", found, displayName, displayDesc)
	} else {
		ctx.Println("Skill `%s` — %q\n", found, displayName)
	}
	// Body is raw Markdown; transcript rendering will handle it.
	// Use Println with %s to preserve exact content line breaks.
	ctx.Println("%s", body)
	return nil
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	// Compare via error string containing the sentinel; avoid importing
	// skills.ErrSkillNotFound logic duplication. The context's LoadSkill
	// wraps that sentinel, so errors.Is would work but we avoid import.
	// Fall back to string check for decoupling.
	return strings.Contains(err.Error(), "skill not found") || strings.Contains(err.Error(), "not found")
}
