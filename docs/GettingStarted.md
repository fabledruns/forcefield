# Getting Started

This page explains how to install Forcefield, configure it, and run your first agent session.

## Requirements

Before you start, make sure that you have:

- Go 1.22 or later, if you build from source
- [Ollama](https://ollama.com) installed and running using `ollama serve`
- At least one local model installed

Example:

```bash
ollama pull ornith:9b
ollama serve
```

## Install

> **Note:**
> A one-line installer (`curl` / PowerShell) is planned but is not available yet.

### Build from source

1. Clone the repository:

```bash
git clone https://github.com/fabledruns/forcefield
cd forcefield
```

2. Build the binary:

```bash
go build -o ff .
```

3. Run Forcefield:

```bash
./ff
```

On Windows PowerShell:

```powershell
go build -o ff.exe .
.\ff.exe
```

### Use a release binary

1. Open the GitHub Releases page for Forcefield.
2. Download the binary for your operating system.
3. Place the binary on your `PATH` if you want the `ff` command available globally.
4. Run `ff`.

## First Run

On first run, Forcefield creates:

```text
~/.forcefield/config.yaml
~/.forcefield/skills/
```

The default configuration uses Ollama:

```yaml
model:
  provider: ollama
  endpoint: http://localhost:11434
  name: ornith:9b

agent:
  name: default
  system_prompt: |
    You are a helpful coding assistant.
```

Edit `config.yaml` if your model name or endpoint is different.

## Start a Chat

```bash
ff
```

Type a prompt:

```text
› explain this repository
```

Forcefield sends the prompt to the local model, runs any requested tools, and shows the streamed reply in the terminal.

## Useful Slash Commands

| Command              | Action                                      |
| -------------------- | ------------------------------------------- |
| `/help`              | List available commands.                    |
| `/model`             | Show the active model.                      |
| `/model <name>`      | Switch model for the next request.          |
| `/provider`          | Show the active provider.                   |
| `/sessions`          | Open the saved session picker.              |
| `/clear`             | Clear the visible transcript.               |
| `/exit`              | End the session.                            |

## One-Shot Prompt

```bash
ff run "summarize the README"
```

This path does not open the interactive TUI. It prints the final model response and exits.

## Add a Skill

1. Create a Markdown file in `~/.forcefield/skills/`.
2. Optionally add YAML frontmatter with `id`, `name`, and `description`.
3. Restart Forcefield so the skill store reloads.
4. Ask the agent a task that needs the skill. The model can load it with `load_skill`.

Example skill:

```md
---
id: clean-code
name: Clean Code
description: Prefer simple, readable code changes.
---

Prefer small functions.
Use clear names.
Avoid unnecessary abstraction.
```

## What Stays Local

Forcefield is local-first. It does not require:

- a user account
- a cloud service
- remote data processing
- telemetry

Your configuration, skills, and sessions stay on your machine.
