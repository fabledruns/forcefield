# Skills

Package: `internal/skills`

The `skills` package discovers, indexes, and loads agent skills from the local skill store. Skills are Markdown files that give the agent extra instructions for a task.

## Purpose

Skills keep specialized guidance out of the base system prompt. Forcefield indexes skill metadata at startup. The model sees a short catalog. The model loads the full skill body only when it needs it.

## Skill Store Location

```text
~/.forcefield/skills/
```

Forcefield creates this directory if it does not exist.

## Skill File Format

A skill is a Markdown file. YAML frontmatter is optional.

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
| `id`          | File name, converted to lowercase kebab-case.          |
| `name`        | First Markdown heading, or the file base name.         |
| `description` | Empty string.                                          |
| body          | Markdown content after frontmatter, or the full file.  |

Empty skill files are ignored.

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

An in-memory index of discovered skills.

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

Scans the skills directory once, parses each Markdown file, and builds the store.

### `Dir`

```go
func Dir(forcefieldHome string) (string, error)
```

Returns the skills directory path and creates it if needed.

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

1. At startup, the runtime builds a skill store.
2. The agent system prompt includes only the catalog.
3. The model may recommend a skill from the catalog without loading it.
4. When the model needs full instructions, it calls the `load_skill` tool with the skill ID.
5. The runtime tool reads the body from the store and returns it to the model.

## Example Skills

The repository includes sample skills under `examples/skills/`:

- `architecture.md`
- `clean-code.md`
- `code-review.md`
- `debugging.md`

Copy a file into `~/.forcefield/skills/` to make it available to the agent.

## Design Notes

- Skills are local files. There is no remote skill registry in this prototype.
- The store indexes once at startup. Restart Forcefield after you add or change skills.
- Frontmatter is preferred for clear IDs and descriptions, but plain Markdown works.
