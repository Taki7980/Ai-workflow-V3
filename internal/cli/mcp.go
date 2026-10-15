package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/version"
)

// mcpMaxMessage bounds one JSON-RPC line read from the client.
const mcpMaxMessage = 8 << 20

var mcpProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

const mcpInstructions = `AI Workflow control plane for this workspace. Call brief (or context) before editing: it returns lane, risk, evidence_state and bounded context. Do not edit while evidence_state is requires_exploration. Use memory_search for durable project facts, memory_add after a verified fix, and verify with focused checks before finishing.`

// mcpArgs is the union of every tool's arguments; each tool reads only its own.
type mcpArgs struct {
	Task         string   `json:"task"`
	Query        string   `json:"query"`
	Format       string   `json:"format"`
	Symbol       string   `json:"symbol"`
	Endpoint     string   `json:"endpoint"`
	ChangedFiles []string `json:"changed_files"`
	WriteHandoff bool     `json:"write_handoff"`
	Limit        int      `json:"limit"`
	Type         string   `json:"type"`
	Keywords     string   `json:"keywords"`
	Summary      string   `json:"summary"`
	Evidence     string   `json:"evidence"`
	Files        []string `json:"files"`
	Confidence   *float64 `json:"confidence"`
	Checks       []string `json:"checks"`
	Text         string   `json:"text"`
	MaxLines     int      `json:"max_lines"`
	MaxChars     int      `json:"max_chars"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	// call maps the arguments onto the matching CLI handler.
	call func(root string, a mcpArgs, out, errOut io.Writer) int
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func strList(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}
func object(required []string, props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}

// anchorFlags renders the shared brief/context anchors as `--name=value`
// so values starting with "-" are never parsed as flags.
func anchorFlags(a mcpArgs) []string {
	var argv []string
	if a.Symbol != "" {
		argv = append(argv, "--symbol="+a.Symbol)
	}
	if a.Endpoint != "" {
		argv = append(argv, "--endpoint="+a.Endpoint)
	}
	for _, f := range a.ChangedFiles {
		argv = append(argv, "--changed-file="+f)
	}
	return argv
}

var anchorProps = map[string]any{
	"task":          str("the task in natural language"),
	"symbol":        str("exact symbol anchor"),
	"endpoint":      str("endpoint anchor"),
	"changed_files": strList("changed files; default: detected from git"),
}

var mcpTools = []mcpTool{
	{
		Name:        "brief",
		Description: "Emit the agent brief for a task: lane, risk, evidence_state (sufficient, requires_exploration, abstain), skill sequence, budget and bounded fresh context. Records a run journal for non-answer lanes.",
		InputSchema: object([]string{"task"}, map[string]any{
			"task": anchorProps["task"], "symbol": anchorProps["symbol"], "endpoint": anchorProps["endpoint"], "changed_files": anchorProps["changed_files"],
			"format":        map[string]any{"type": "string", "enum": []string{"json", "markdown", "prompt"}, "description": "output format (default json)"},
			"write_handoff": map[string]any{"type": "boolean", "description": "write ai-workspace/handoff/HANDOFF.md for non-answer lanes"},
		}),
		call: func(root string, a mcpArgs, out, errOut io.Writer) int {
			format := a.Format
			if format == "" {
				format = "json"
			}
			argv := append([]string{"--format=" + format, "--write-handoff=" + strconv.FormatBool(a.WriteHandoff)}, anchorFlags(a)...)
			return briefCmd(root, append(argv, "--", a.Task), out, errOut)
		},
	},
	{
		Name:        "context",
		Description: "Gather bounded evidence for a task without writing any workspace state: lane, risk, retrieval plan and context items.",
		InputSchema: object([]string{"task"}, anchorProps),
		call: func(root string, a mcpArgs, out, errOut io.Writer) int {
			return contextCmd(root, append(anchorFlags(a), "--", a.Task), out, errOut)
		},
	},
	{
		Name:        "route",
		Description: "Classify a task into lane, risk and retrieval intent without retrieving context.",
		InputSchema: object([]string{"task"}, map[string]any{"task": anchorProps["task"]}),
		call: func(root string, a mcpArgs, out, errOut io.Writer) int {
			return route(root, []string{a.Task}, out, errOut)
		},
	},
	{
		Name:        "search",
		Description: "Search indexed symbols across included repositories.",
		InputSchema: object([]string{"query"}, map[string]any{"query": str("symbol or keyword query")}),
		call: func(root string, a mcpArgs, out, errOut io.Writer) int {
			return searchCmd(root, []string{a.Query}, out, errOut)
		},
	},
	{
		Name:        "memory_search",
		Description: "Search durable project memory (decisions, verified fixes, constraints).",
		InputSchema: object([]string{"query"}, map[string]any{
			"query": str("search text"),
			"limit": map[string]any{"type": "integer", "minimum": 1, "description": "maximum results (default memory.max_results)"},
		}),
		call: func(root string, a mcpArgs, out, errOut io.Writer) int {
			return memoryCmd(root, []string{"search", "--limit=" + strconv.Itoa(a.Limit), "--", a.Query}, out, errOut)
		},
	},
	{
		Name:        "memory_add",
		Description: "Record a reusable, verified project fact in durable memory.",
		InputSchema: object([]string{"type", "keywords", "summary"}, map[string]any{
			"type":       map[string]any{"type": "string", "enum": []string{"decision", "incident", "verified-fix", "architecture", "pattern", "optimization", "constraint"}},
			"keywords":   str("search keywords"),
			"summary":    str("the reusable fact"),
			"evidence":   str("supporting evidence"),
			"files":      strList("related workspace files"),
			"confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "default 0.8"},
		}),
		call: func(root string, a mcpArgs, out, errOut io.Writer) int {
			argv := []string{"add", "--type=" + a.Type, "--keywords=" + a.Keywords, "--summary=" + a.Summary, "--evidence=" + a.Evidence}
			if a.Confidence != nil {
				argv = append(argv, "--confidence="+strconv.FormatFloat(*a.Confidence, 'f', -1, 64))
			}
			for _, f := range a.Files {
				argv = append(argv, "--file="+f)
			}
			return memoryCmd(root, argv, out, errOut)
		},
	},
	{
		Name:        "verify",
		Description: "Run focused check commands (no shell, bounded output, timeouts) plus handoff validation. Each check is a command line such as \"go test ./...\". It executes programs in the workspace.",
		InputSchema: object([]string{"checks"}, map[string]any{"checks": strList("check commands to run")}),
		call: func(root string, a mcpArgs, out, errOut io.Writer) int {
			argv := []string{"--strict"}
			for _, c := range a.Checks {
				argv = append(argv, "--check="+c)
			}
			return verifyCmd(root, argv, out, errOut)
		},
	},
	{
		Name:        "handoff_validate",
		Description: "Validate ai-workspace/handoff/HANDOFF.md.",
		InputSchema: object([]string{}, map[string]any{}),
		call: func(root string, _ mcpArgs, out, errOut io.Writer) int {
			return handoffCmd(root, nil, out, errOut)
		},
	},
	{
		Name:        "compress",
		Description: "Bound noisy text such as test or build logs: keeps the first 75% of max_lines and the last lines, omitting the middle.",
		InputSchema: object([]string{"text"}, map[string]any{
			"text":      str("text to compress"),
			"max_lines": map[string]any{"type": "integer", "minimum": 1, "description": "default 80"},
			"max_chars": map[string]any{"type": "integer", "minimum": 1, "description": "default 12000"},
		}),
		call: func(_ string, a mcpArgs, out, errOut io.Writer) int {
			argv := []string{}
			if a.MaxLines != 0 {
				argv = append(argv, "--max-lines="+strconv.Itoa(a.MaxLines))
			}
			if a.MaxChars != 0 {
				argv = append(argv, "--max-chars="+strconv.Itoa(a.MaxChars))
			}
			return compressCmd(argv, strings.NewReader(a.Text), out, errOut)
		},
	},
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// mcpCmd serves the workflow commands as MCP tools over stdio
// (newline-delimited JSON-RPC 2.0) so agents call them natively.
func mcpCmd(root string, args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(errOut, "mcp takes no arguments")
		return 2
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), mcpMaxMessage)
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if resp := mcpHandle(root, line); resp != nil {
			if err := enc.Encode(resp); err != nil {
				fmt.Fprintln(errOut, err)
				return 1
			}
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

// mcpHandle answers one JSON-RPC message; nil means no reply (notification).
func mcpHandle(root string, line []byte) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}}
	}
	if len(req.ID) == 0 {
		return nil
	}
	resp := &rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if req.JSONRPC != "2.0" || req.Method == "" {
		resp.Error = &rpcError{-32600, "invalid request"}
		return resp
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p) // a missing version falls back to the latest
		v := mcpProtocolVersions[0]
		if slices.Contains(mcpProtocolVersions, p.ProtocolVersion) {
			v = p.ProtocolVersion
		}
		resp.Result = map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "ai-workflow", "version": version.Version},
			"instructions":    mcpInstructions,
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": mcpTools}
	case "tools/call":
		result, err := mcpCall(root, req.Params)
		if err != nil {
			resp.Error = &rpcError{-32602, err.Error()}
		} else {
			resp.Result = result
		}
	default:
		resp.Error = &rpcError{-32601, "method not found: " + req.Method}
	}
	return resp
}

// mcpCall runs one tool. Unknown tools and malformed arguments are protocol
// errors; a failing command is a tool result with isError so the agent sees why.
func mcpCall(root string, params json.RawMessage) (map[string]any, error) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, errors.New("invalid params")
	}
	i := slices.IndexFunc(mcpTools, func(t mcpTool) bool { return t.Name == p.Name })
	if i < 0 {
		return nil, fmt.Errorf("unknown tool %q", p.Name)
	}
	var a mcpArgs
	if len(p.Arguments) > 0 && string(p.Arguments) != "null" {
		if err := json.Unmarshal(p.Arguments, &a); err != nil {
			return nil, fmt.Errorf("invalid arguments for %s: %v", p.Name, err)
		}
	}
	var out, errOut bytes.Buffer
	code := mcpTools[i].call(root, a, &out, &errOut)
	text := strings.TrimSpace(out.String())
	if code != 0 {
		text = strings.TrimSpace(strings.Join([]string{text, errOut.String(), "exit code " + strconv.Itoa(code)}, "\n"))
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": code != 0}, nil
}
