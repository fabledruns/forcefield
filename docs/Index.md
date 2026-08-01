# Documentation for Forcefield

Forcefield is an open source, lightweight, local-first agent harness for running specialized AI agents. It is written in Go. This is the documentation for Forcefield.
You can find the index of the contents in this file.

## Guides

| Name                                        | Description                                                         |
| ------------------------------------------- | ------------------------------------------------------------------- |
| [Getting Started](GettingStarted.md) | Install Forcefield, configure Ollama, and run your first session. |
| [CLI](CLI.md)                        | Command-line entry points: `ff`, `ff chat`, `ff run`, and resume. |

## Packages

| Name                      | Package     | Description                                                                                                     |
| ------------------------- | ----------- | --------------------------------------------------------------------------------------------------------------- |
| [Agent](Agent.md)         | `agent`     | Defines the `Agent` type and builds the final system prompt by combining the agent identity with loaded skills. |
| [Command](Command.md)     | `command`   | Implements slash command parsing and execution for interactive chat.                                            |
| [Config](Config.md)       | `config`    | Loads, validates, and manages Forcefield's global YAML configuration file.                                      |
| [Providers](Providers.md) | `providers` | Defines the model provider interface and its implementations for communicating with LLMs.                       |
| [Runtime](Runtime.md)     | `runtime`   | Coordinates agents, providers, tools, skills, and sessions to execute model interactions.                       |
| [Session](Session.md)     | `session`   | Manages chat sessions, message history, persistence, and provider-specific message conversion.                  |
| [Skills](Skills.md)       | `skills`    | Discovers, indexes, and loads agent skills on demand from the local skill store.                                |
| [Tools](Tools.md)         | `tools`     | Defines the tool framework, including tool registration, execution, and built-in tool implementations.          |
| [TUI](TUI.md)             | `tui`       | Provides the interactive terminal interface built with Bubble Tea for chatting with Forcefield.                 |

## Installation

> **Note:**
> A one-line installer (`curl`/PowerShell) is planned but is not available yet.

To install Forcefield on your machine, you can either clone the repository and build your own binary, or go to the Releases section and grab a binary from there.
Forcefield will be automatically be installed, and you can use the `ff` command to run Forcefield.

For a full first-run walkthrough, see [Getting Started](GettingStarted.md).

### Make Your Own Binary

Clone the repository using `git clone https://github.com/fabledruns/forcefield` in a new folder, and run `go build -o ff .`, then run using `./ff`.

### Releases Binary

Download the latest binary from the GitHub Releases page for your operating system.

## Architecture

Forcefield follows a modular architecture where each package has a single responsibility. The runtime coordinates the other packages to process a user request, execute tools, and return a response.

```text
             User
               │
               ▼
        CLI / Interactive TUI
               │
               ▼
            Runtime
     ┌─────────┼─────────┐
     │         │         │
     ▼         ▼         ▼
   Agent    Session    Config
     │
     ▼
   Provider
     │
     ▼
 Local LLM (Ollama)
     │
     ▼
 Tool Calls
     │
     ▼
    Tools
     │
     ▼
  Skill Loading
     │
     ▼
 Final Response
```

### Request Flow

1. The user submits a prompt through the CLI or interactive TUI.
2. The runtime loads the configuration and restores the current session.
3. The runtime builds the agent and its initial system prompt.
4. The selected model provider sends the request to the local language model.
5. If the model requests a tool, the runtime executes it and returns the result to the model.
6. If the model requests a skill, the skill is loaded from disk and provided to the model.
7. The model produces its final response.
8. The runtime saves the updated session and returns the response to the user.