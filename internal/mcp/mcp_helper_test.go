package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMCPHelperProcess is re-executed as a child MCP server by the Host
// integration tests (the standard library helper-process pattern,
// portable across Windows and Unix because the child is the test binary
// itself, never a shell). It is never run as a real test: without
// MCP_HELPER_PROCESS=1 it returns immediately.
//
// Protocol: line-delimited JSON-RPC on stdin/stdout, diagnostics on
// stderr. Environment knobs (all MCP_HELPER_*):
//
//	MODE: serve (default), heartbeat
//	VERSION: protocolVersion in the initialize result (default: ClientVersion)
//	NO_TOOLS=1: omit the tools capability
//	TOOLS_JSON: verbatim tools array contents for tools/list responses
//	  (comma-separated entries, no outer brackets)
//	LIST_ERROR=1: answer tools/list with a JSON-RPC error
//	PAGED=1: two-page tools/list (t1/cursor p1, then t2)
//	CURSOR_LOOP=1: every tools/list page returns nextCursor "c0"
//	CALL_ERROR=1: tools/call answers with isError true
//	CALL_SLEEP_MS=N: sleep before each tools/call response
//	HANG=1: never answer initialize (startup-timeout tests)
//	EXIT_EARLY=N: exit with code N immediately
//	CRASH_AFTER_INIT=1: answer initialize, then exit 1
//	DIE_ON_CALL=1: exit 1 when tools/call arrives
//	STDERR_FILL=N: write N KiB of 'E' to stderr at start, then serve
//	STDERR_MSG=S: one stderr line at start, then serve
//	STDERR_PWD=1: print the working directory to stderr at start
//	ENV_DUMP=1: print the full environment (sorted K=V) to stderr
//	ARGV_DUMP=1: print os.Args to stderr as JSON, then exit 0
//	IGNORE_STDIN=1: never exit on stdin EOF (kill-path tests)
//	CLOSE_STDOUT=1: close stdout without exiting (EOF-while-alive tests)
//	TREE=1 (+HELPER_LOG=path): spawn a heartbeat grandchild, then block
//	LOG: heartbeat tick file (heartbeat mode)
func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("MCP_HELPER_PROCESS") != "1" {
		return
	}
	os.Exit(mcpHelperMain())
}

func mcpHelperMain() int {
	get := func(k, def string) string {
		if v, ok := os.LookupEnv(k); ok {
			return v
		}
		return def
	}
	switch get("MCP_HELPER_MODE", "serve") {
	case "heartbeat":
		return mcpHeartbeat(get("MCP_HELPER_LOG", ""))
	case "serve":
		// Continue below.
	default:
		fmt.Fprintln(os.Stderr, "unknown MCP_HELPER_MODE")
		return 2
	}

	if n, err := strconv.Atoi(get("MCP_HELPER_EXIT_EARLY", "")); err == nil {
		os.Exit(n)
	}
	if get("MCP_HELPER_ARGV_DUMP", "") == "1" {
		enc, _ := json.Marshal(os.Args)
		fmt.Fprintln(os.Stderr, string(enc))
		return 0
	}
	if get("MCP_HELPER_STDERR_PWD", "") == "1" {
		wd, _ := os.Getwd()
		fmt.Fprintln(os.Stderr, "cwd="+wd)
	}
	if msg := get("MCP_HELPER_STDERR_MSG", ""); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	if fill, err := strconv.Atoi(get("MCP_HELPER_STDERR_FILL", "")); err == nil && fill > 0 {
		chunk := strings.Repeat("E", 8192)
		for written := 0; written < fill; written += 8 {
			fmt.Fprint(os.Stderr, chunk)
		}
	}
	if get("MCP_HELPER_ENV_DUMP", "") == "1" {
		for _, kv := range sortedEnv() {
			fmt.Fprintln(os.Stderr, kv)
		}
	}
	if get("MCP_HELPER_TREE", "") == "1" {
		if err := mcpSpawnHeartbeat(get("MCP_HELPER_LOG", "")); err != nil {
			fmt.Fprintln(os.Stderr, "spawn heartbeat:", err)
			return 9
		}
		sleepForever()
		return 0
	}
	if get("MCP_HELPER_IGNORE_STDIN", "") == "1" {
		// Drain stdin without acting so the pipe never back-pressures,
		// but never exit: only tree termination ends this mode.
		go func() {
			buf := make([]byte, 4096)
			for {
				if _, err := os.Stdin.Read(buf); err != nil {
					return
				}
			}
		}()
		sleepForever()
		return 0
	}
	if get("MCP_HELPER_CLOSE_STDOUT", "") == "1" {
		// Close stdout without exiting: the parent must observe EOF as a
		// terminal transport failure while the child is still alive, and
		// a later Close must still terminate the whole tree. Stdin is
		// drained so parent writes never block the observation.
		_ = os.Stdout.Close()
		go func() {
			buf := make([]byte, 4096)
			for {
				if _, err := os.Stdin.Read(buf); err != nil {
					return
				}
			}
		}()
		sleepForever()
		return 0
	}
	if get("MCP_HELPER_HANG", "") == "1" {
		sleepForever()
		return 0
	}
	return mcpServe(get)
}

func sleepForever() {
	for {
		time.Sleep(time.Hour)
	}
}

func mcpHeartbeat(log string) int {
	if log == "" {
		fmt.Fprintln(os.Stderr, "heartbeat needs MCP_HELPER_LOG")
		return 9
	}
	f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "heartbeat open log:", err)
		return 9
	}
	defer f.Close()
	for {
		if _, err := f.WriteString("tick\n"); err != nil {
			return 9
		}
		if err := f.Sync(); err != nil {
			return 9
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// mcpSpawnHeartbeat re-executes the test binary as a detached heartbeat
// grandchild. No shell, no scripts: portable across Windows and Unix.
// The child is deliberately never waited on; tree termination reaps it.
func mcpSpawnHeartbeat(log string) error {
	if log == "" {
		return fmt.Errorf("tree helper needs MCP_HELPER_LOG")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMCPHelperProcess$")
	cmd.Env = append([]string{}, "MCP_HELPER_PROCESS=1", "MCP_HELPER_MODE=heartbeat", "MCP_HELPER_LOG="+log)
	cmd.Stdout, cmd.Stderr = nil, nil
	cmd.Stdin = nil
	return cmd.Start()
}

func sortedEnv() []string {
	env := os.Environ()
	for i := 0; i < len(env); i++ {
		for j := i + 1; j < len(env); j++ {
			if env[j] < env[i] {
				env[i], env[j] = env[j], env[i]
			}
		}
	}
	return env
}

type helperEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func mcpReply(id json.RawMessage, payload string) {
	fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%s,%s}\n", string(id), payload)
}

func mcpServe(get func(string, string) string) int {
	version := get("MCP_HELPER_VERSION", ClientVersion)
	noTools := get("MCP_HELPER_NO_TOOLS", "") == "1"
	toolsJSON := get("MCP_HELPER_TOOLS_JSON", "")
	paged := get("MCP_HELPER_PAGED", "") == "1"
	cursorLoop := get("MCP_HELPER_CURSOR_LOOP", "") == "1"
	callError := get("MCP_HELPER_CALL_ERROR", "") == "1"
	callSleepMS, _ := strconv.Atoi(get("MCP_HELPER_CALL_SLEEP_MS", ""))
	dieOnCall := get("MCP_HELPER_DIE_ON_CALL", "") == "1"
	crashAfterInit := get("MCP_HELPER_CRASH_AFTER_INIT", "") == "1"
	listError := get("MCP_HELPER_LIST_ERROR", "") == "1"

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytesTrimSpace(line)) == 0 {
			continue
		}
		var env helperEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			continue // malformed input is ignored, never fatal
		}
		switch env.Method {
		case MethodInitialize:
			caps := `{"tools":{}}`
			if noTools {
				caps = `{"resources":{}}`
			}
			mcpReply(env.ID, fmt.Sprintf(`"result":{"protocolVersion":%s,"capabilities":%s,"serverInfo":{"name":"fake-mcp","version":"1"}}`,
				quoteJSON(version), caps))
			if crashAfterInit {
				return 1
			}
		case MethodInitialized:
			// Notification: no reply.
		case MethodListTools:
			if listError {
				mcpReply(env.ID, `"error":{"code":-32000,"message":"list blew up"}}`)
				continue
			}
			var params ListToolsParams
			_ = json.Unmarshal(env.Params, &params)
			tools, cursor := helperToolsPage(toolsJSON, paged, cursorLoop, params.Cursor)
			next := ""
			if cursor != "" {
				next = fmt.Sprintf(`,"nextCursor":%s`, quoteJSON(cursor))
			}
			mcpReply(env.ID, fmt.Sprintf(`"result":{"tools":[%s]%s}`, tools, next))
		case MethodCallTool:
			if dieOnCall {
				os.Exit(1)
			}
			if callSleepMS > 0 {
				time.Sleep(time.Duration(callSleepMS) * time.Millisecond)
			}
			var params CallToolParams
			_ = json.Unmarshal(env.Params, &params)
			args, _ := json.Marshal(params.Arguments)
			isErr := "false"
			if callError {
				isErr = "true"
			}
			mcpReply(env.ID, fmt.Sprintf(`"result":{"content":[{"type":"text","text":%s}],"isError":%s}`,
				quoteJSON("echo:"+string(args)), isErr))
		default:
			if len(env.ID) > 0 {
				mcpReply(env.ID, `"error":{"code":-32601,"message":"Method not found"}}`)
			}
		}
	}
	// stdin EOF: graceful exit for well-behaved servers.
	return 0
}

func helperToolsPage(override string, paged, cursorLoop bool, cursor *string) (string, string) {
	if override != "" {
		return override, ""
	}
	if cursorLoop {
		return `{"name":"loop","description":"loops","inputSchema":{"type":"object"}}`, "c0"
	}
	if paged {
		if cursor != nil && *cursor == "p1" {
			return `{"name":"t2","description":"second","inputSchema":{"type":"object"}}`, ""
		}
		return `{"name":"t1","description":"first","inputSchema":{"type":"object"}}`, "p1"
	}
	return `{"name":"echo","description":"Echo back arguments","inputSchema":{"type":"object"}}`, ""
}

func bytesTrimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && (b[start] == ' ' || b[start] == '\t' || b[start] == '\n' || b[start] == '\r') {
		start++
	}
	end := len(b)
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\n' || b[end-1] == '\r') {
		end--
	}
	return b[start:end]
}

// quoteJSON comes from transport_test.go (shared test scope).
