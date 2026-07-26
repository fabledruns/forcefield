# Forcefield TODO Roadmap

## Completed

### Tool System Foundation
[x] Create Tool interface abstraction
[x] Add Tool Result and Definition types
[x] Create tool Registry system
[x] Create tool Manager execution layer
[x] Add tool argument helpers
[x] Add tool-specific error handling
[x] Add builtin tool wiring system

### Built-in Tools
[x] Implement filesystem tools
    - read_file
    - write_file
    - list_files

[x] Implement shell tools
    - pwd

### Testing & Quality
[x] Add unit tests for tools
[x] Add registry tests
[x] Add manager tests
[x] Add builtin wiring tests
[x] Run go test ./...
[x] Verify build succeeds
[x] Create smoketest command for manual testing

### Provider Architecture
[x] Refactor provider API from:
    system string + prompt string

    to:

    []Message conversation format

[x] Add provider message types
    - Role
    - Message
    - ToolCall
    - Response

[x] Update Runtime to use message-based conversations
[x] Update Ollama provider to accept message history

---

# In Progress

## Tool Calling

[ ] Add tool definitions to Ollama request
[ ] Convert tools.Definition → Ollama tool schema
[ ] Parse tool_calls from Ollama responses
[ ] Return ToolCall data through provider Response

---

# Agent Loop

[ ] Add conversation state/history management
[ ] Add tool execution through tools.Manager
[ ] Append tool results back into conversation
[ ] Implement multi-step tool execution loop
[ ] Stop loop when model returns final response

---

# Future

## Memory
[ ] Add session storage
[ ] Add conversation persistence
[ ] Add agent memory system

## Tool Ecosystem
[ ] Add configurable tool permissions
[ ] Add tool timeout handling
[ ] Add MCP support
[ ] Add plugin-based tools

## Performance
[ ] Benchmark startup time
[ ] Optimize tool execution path
[ ] Profile agent loop latency