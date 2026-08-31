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

### Linux / macOS

```bash
curl -fsSL https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.sh | sh
```

Supports `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`. The installer auto-detects OS/arch, downloads from [GitHub Releases](https://github.com/fabledruns/forcefield/releases), verifies `checksums.txt`, installs to `~/.local/bin`, and adds that directory to `PATH` if needed (no `sudo`).

Pin to a version:

```bash
curl -fsSL https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.sh | sh -s -- --version v1.0.0
# or
FORCEFIELD_VERSION=v1.0.0 sh scripts/install.sh
```

### Windows

```powershell
irm https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.ps1 | iex
```

Installs to `$HOME\.local\bin` and adds it to your **user** `PATH` (no admin). Pin a version:

```powershell
$env:FORCEFIELD_VERSION="v1.0.0"; irm https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.ps1 | iex
```

Run the installer again to upgrade in place. Both installers are safe to run multiple times and never touch `~/.forcefield`.

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

### Manual download

1. Open the [GitHub Releases](https://github.com/fabledruns/forcefield/releases) page.
2. Download the binary for your OS/arch (`ff-linux-amd64`, `ff-darwin-arm64`, `ff-windows-amd64.exe`, etc. plus `checksums.txt`).
3. Verify the checksum (`sha256sum -c checksums.txt` or `Get-FileHash` on Windows).
4. Place the binary on your `PATH` as `ff` (`ff.exe` on Windows) and `chmod +x` on Linux/macOS.
5. Run `ff --version` and `ff doctor`.

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
    You are Forcefield, a local-first coding agent. Complete software tasks in real repositories: inspect, change, run, debug, and verify. Prefer a working, minimal result over advice or extra architecture.
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
| `/provider <name>`   | Switch provider for the next request.       |
| `/sessions`          | Open the saved session picker.              |
| `/skills`            | List available skills.                      |
| `/skills show <id>`  | Display one skill's full instructions.      |
| `/clear`             | Clear the visible transcript.               |
| `/exit`              | End the session.                            |

Provider and model switches take effect immediately - no restart needed.

## Using Other Providers

Forcefield is not limited to Ollama. It speaks the OpenAI Chat Completions protocol (LM Studio, OpenRouter, NVIDIA NIM, Groq, Mistral, Together AI, xAI, OpenAI, and any self-hosted compatible server), Anthropic's native API, and Google Gemini.

To add a provider, extend `providers:` in `~/.forcefield/config.yaml`:

```yaml
providers:
  openai:
    type: openai

  local-llm:
    type: openai-compatible
    base_url: http://localhost:1234/v1
```

API keys come from environment variables or `.env` files - never from config.yaml:

```bash
export OPENAI_API_KEY="sk-..."
```

Then switch inside a session with `/provider`, or set `model.provider: openai` in the config first. See [Config](Config.md) and [Providers](Providers.md) for all supported services and options, and run `ff doctor` to verify reachability.

## One-Shot Prompt

```bash
ff run "summarize the README"
```

This path does not open the interactive TUI. It prints the final model response and exits.

## Add a Skill

Skills are global-only (`~/.forcefield/skills/`). Two layouts are supported:

- File skill: `~/.forcefield/skills/clean-code.md`
- Directory skill: `~/.forcefield/skills/git-review/SKILL.md` (supporting files may sit alongside)

Steps:

1. Create a Markdown file (or `SKILL.md` directory) under `~/.forcefield/skills/`.
2. Optionally add YAML frontmatter with `id`, `name`, and `description`.
3. Restart Forcefield so the global skill store reloads.
4. Verify with `/skills list` and `/skills show <id>`.
5. Ask the agent a task that needs the skill. The model can load it with `load_skill`.

Example skill (`~/.forcefield/skills/clean-code.md`):

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
