# Skills

Package: `internal/skills`

The `skills` package discovers, indexes, and loads global agent skills from the local skill store. Skills are Markdown files that give the agent extra instructions for a task. They are filesystem-first, global-only, and local-only.

## Purpose

Skills keep specialized guidance out of the base system prompt. Forcefield indexes skill metadata at startup. The model sees a short catalog. The model loads the full skill body only when it needs it. The user inspects skills via `/skills` without affecting model state.

## Skill Store Location

```text
~/.forcefield/skills/
```

Forcefield creates this directory if it does not exist. Skills are **global only** — there is no project-local skill directory. This keeps skill identity deterministic and avoids per-project precedence complexity.

Two layouts are indexed:

```text
~/.forcefield/skills/review.md                # file skill
~/.forcefield/skills/git-review/SKILL.md      # directory skill
```

For a directory skill, only `SKILL.md` (case-insensitive `skill.md`) is indexed. Supporting files alongside it (templates, scripts, examples) are never indexed and never executed automatically — the model may read them via `read_file` if needed and permissions allow.

## Skill File Format

A skill is a Markdown file. YAML frontmatter is optional. Directory skills use the `SKILL.md` file as the skill body; its frontmatter follows the same rules.

### With frontmatter

```md
---
id: code-review
name: Code Review
description: Review code for correctness and maintainability.
---

Review in this order:

1. Correctness
2. Security
3. Performance
```

### Without frontmatter

```md
# Go Development

Use the Go language standard.
Prefer simple designs.
```

### Metadata rules

| Field         | Source if missing                                      |
| ------------- | ------------------------------------------------------ |
| `id`          | File name (or directory name for `SKILL.md`), converted to lowercase kebab-case via `NormalizeID`. |
| `name`        | First Markdown heading, or the file base name.         |
| `description` | Empty string.                                          |
| body          | Markdown content after frontmatter, or the full file.  |

Empty skill files are ignored. Files larger than 1 MiB are skipped to keep startup predictable. Empty `id` after normalization (e.g. a file named `---.md`) is also skipped.

## Types

### `Skill`

A catalog entry.

| Field         | Description                          |
| ------------- | ------------------------------------ |
| `ID`          | Stable skill identifier.             |
| `Name`        | Display name.                        |
| `Description` | Short summary for the catalog.       |
| `Path`        | Absolute path to the Markdown file.  |

### `Store`

An in-memory index of discovered global skills.

| Method      | Description                                      |
| ----------- | ------------------------------------------------ |
| `Catalog()` | Returns a copy of all indexed skills.            |
| `Get(id)`   | Looks up one skill by ID.                        |
| `Load(id)`  | Reads and returns the Markdown body for one skill. |

## Main Functions

### `New`

```go
func New(forcefieldHome string) (*Store, error)
```

Scans the global skills directory once, parses each Markdown file and each `SKILL.md` directory skill, and builds the store. Skips empty files, oversized files (>1 MiB), non-markdown files, and symlinks that escape the skills directory. Deterministic ordering is by normalized sort key (lower-cased base name). Duplicate `id` values keep both catalog entries but `Get`/`Load` resolve to the first alphabetical file (first catalog entry) for that id.

### `Dir`

```go
func Dir(forcefieldHome string) (string, error)
```

Returns the global skills directory path and creates it if needed.

### `FormatCatalog`

```go
func FormatCatalog(catalog []Skill) string
```

Renders the catalog as text for the agent system prompt.

Example output shape:

```text
- id: `code-review`, name: "Code Review" — Review code for correctness and maintainability.
```

## On-Demand Loading

1. At startup, the runtime builds a global skill store.
2. Each specialised agent's system prompt includes only its assigned catalog (`general` sees all; others see their allow-list). See [Agents](Agents.md).
3. The model may recommend a skill from the catalog without loading it.
4. When the model needs full instructions, it calls the `load_skill` tool with the skill ID.
5. The runtime tool reads the body from the store and returns it to the model — but only for IDs in the active agent's set. Other IDs fail soft (unassigned vs absent are reported distinctly), matched exactly with no normalization fallthrough.

## Per-Agent Assignment

Agents declare skill IDs in their definition (`Skills`, or `AllSkills` for `general`); `agents.<name>.skills` in config replaces the assignment (omitted keeps, `[]` means none). Assigned IDs missing from the store are omitted from the catalog and reported by `ff doctor` as warnings — never fatal, never fabricated.

## Slash Commands

| Command              | Action                                                      |
| -------------------- | ----------------------------------------------------------- |
| `/skills`            | List available global skills (alias for `list`).            |
| `/skills list`       | List available global skills.                               |
| `/skills show <id>`  | Display the full Markdown body for one skill.               |

`/skills` is visibility only — it never injects a skill into the model context. The model still loads skills via `load_skill` when relevant. Case-insensitive and kebab-normalized lookup is applied, so `/skills show Go Style Guide` resolves to `go-style-guide`.

## Example Skills

The repository includes sample skills under `examples/skills/`:

- `architecture.md`
- `clean-code.md`
- `code-review.md`
- `debugging.md`

Copy a file into `~/.forcefield/skills/` (or a directory like `~/.forcefield/skills/git-review/SKILL.md`) to make it available to the agent.

## Security

- Supporting files are never executed merely because they exist in the skills directory.
- Skill content cannot bypass tool permissions, shell restrictions, or sandboxing — a skill that asks the model to run a destructive command still goes through the `ask`/`deny` permission flow and the configured `sandbox` executor.
- Symlinked skill files or directories that resolve outside `~/.forcefield/skills/` are ignored. Path traversal segments (`..`, `/`) in an id are rejected by `Load`.
- Oversized skill files (>1 MiB) are skipped. The catalog is capped at 256 entries.

## Design Notes

- Skills are global, local files. There is no remote skill registry in this prototype and no project-local skill directory.
- The store indexes once at startup. Restart Forcefield after you add or change skills.
- Frontmatter is preferred for clear IDs and descriptions, but plain Markdown works.
- `ff doctor` reports the global skill count: `skills: N loaded from ~/.forcefield/skills`.
