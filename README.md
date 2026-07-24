# Forcefield (`ff`)

A minimal, local-first CLI harness for running AI agents against a local
model. This is a **prototype**, not a framework: it proves one idea — that
config + skills + a model call is enough to feel like a real agent harness
— and nothing more.

## What it does

```
ff run "explain this repository"
```

1. Loads `~/.forcefield/config.yaml` (created automatically on first run)
2. Loads every `.md` file in `~/.forcefield/skills/`
3. Appends the skills to the agent's system prompt
4. Sends the system prompt + your task to a local Ollama model
5. Prints the response

That's the entire loop. No memory, no tools, no planning, no multi-agent,
no RAG. Just the harness.

## Requirements

- Go 1.22+
- [Ollama](https://ollama.com) running locally with a model pulled

## Build

```bash
go mod tidy      # resolves and downloads gopkg.in/yaml.v3
go build -o ff ./cmd/ff
```

This produces a single binary, `ff`, in the current directory. Move it
onto your `$PATH` if you want it available everywhere:

```bash
mv ff /usr/local/bin/ff
```

## Configure Ollama

Make sure Ollama is installed and running, and that you've pulled a model:

```bash
ollama serve            # if not already running as a background service
ollama pull llama3
```

Forcefield talks to Ollama's `/api/chat` endpoint at
`http://localhost:11434` by default — this matches Ollama's default
listen address, so most users won't need to change anything.

## Configuration file

On first run, `ff` creates `~/.forcefield/config.yaml`:

```yaml
model:
  provider: ollama
  endpoint: http://localhost:11434
  name: llama3

agent:
  name: default
  system_prompt: |
    You are a helpful coding assistant.
```

Edit `model.name` to match whatever you've pulled in Ollama (e.g.
`llama3.1:8b`, `qwen2.5-coder:7b`, `mistral`), and edit
`agent.system_prompt` to change the agent's base persona.

## Skills

Skills are plain Markdown files dropped into `~/.forcefield/skills/`.
Every `.md` file in that directory is loaded and appended to the system
prompt on every run, sorted by filename.

Example files are provided under `examples/skills/` in this repo — copy
them in to try it out:

```bash
mkdir -p ~/.forcefield/skills
cp examples/skills/*.md ~/.forcefield/skills/
```

```markdown
# Go

You are an expert Go developer.
Always prefer idiomatic Go.
Avoid unnecessary abstractions.
Prefer composition over inheritance.
```

There's no frontmatter, no metadata, no selective loading — every skill
file always applies. That's a deliberate simplification for this
prototype, not a final design.

## Run it

```bash
ff run "explain this repository"
```

```bash
ff run "write a haiku about goroutines"
```

If Ollama isn't running, `ff` fails with a clear message telling you so,
rather than a raw connection-refused stack trace.

## Project structure

```
forcefield/
├── cmd/
│   └── ff/
│       └── main.go          # CLI entrypoint, arg parsing, no business logic
├── internal/
│   ├── agent/                # Agent type: combines base prompt + skills
│   ├── config/                # Loads/creates ~/.forcefield/config.yaml
│   ├── providers/             # ModelProvider interface + Ollama implementation
│   ├── skills/                 # Loads and concatenates *.md skill files
│   └── runtime/                # Wires the above together for one `run`
├── examples/
│   └── skills/                 # Example skill files to copy into ~/.forcefield/skills
├── go.mod
└── README.md
```

Roughly 500 lines of Go, no third-party dependency beyond a YAML parser.

## Extending this later

The seams are intentionally in place for exactly the features this
prototype does *not* implement:

- **Another model provider**: implement `providers.ModelProvider`
  (`Chat(system, prompt string) (string, error)`) and add a `case` in
  `runtime.newProvider`. Nothing else changes.
- **Tool calling**: would live as a new step between "call model" and
  "print response" in `runtime.Run`, likely requiring the provider
  interface to grow a richer response type than a plain string.
- **Memory**: would be a new package read/written by `runtime.Run`
  before/after the model call — the current single-shot flow doesn't
  need it, but the seam is exactly there.
- **Per-skill selection instead of load-everything**: would live in the
  `skills` package alone; nothing outside it needs to know skills went
  from "always all" to "selected."

None of that is built here on purpose — this prototype exists to answer
one question: does the *harness itself* feel useful before any of that
complexity is added?
