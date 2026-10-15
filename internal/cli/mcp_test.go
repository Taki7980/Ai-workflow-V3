package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// mcpSession sends newline-delimited JSON-RPC lines and returns the replies by id.
func mcpSession(t *testing.T, root string, lines ...string) map[string]map[string]any {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := RunWithStdin([]string{"--root", root, "mcp"}, strings.NewReader(strings.Join(lines, "\n")+"\n"), &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	replies := map[string]map[string]any{}
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var r map[string]any
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatalf("reply %q: %v", l, err)
		}
		id, _ := json.Marshal(r["id"])
		replies[string(id)] = r
	}
	return replies
}

func toolResult(t *testing.T, r map[string]any) (string, bool) {
	t.Helper()
	res, ok := r["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", r)
	}
	content := res["content"].([]any)[0].(map[string]any)
	return content["text"].(string), res["isError"].(bool)
}

func TestMCPProtocolAndTools(t *testing.T) {
	root := t.TempDir()
	r := mcpSession(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"route","arguments":{"task":"fix payment validation"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"memory_add","arguments":{"type":"verified-fix","keywords":"payment retry","summary":"-retry uses jittered backoff"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"memory_search","arguments":{"query":"payment retry"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"brief","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"nope","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"nope"}`,
		`not json`,
	)
	if len(r) != 9 { // the notification gets no reply
		t.Fatalf("got %d replies, want 9: %v", len(r), r)
	}
	if v := r["1"]["result"].(map[string]any)["protocolVersion"]; v != "2025-03-26" {
		t.Fatalf("protocolVersion=%v", v)
	}
	if n := len(r["2"]["result"].(map[string]any)["tools"].([]any)); n != len(mcpTools) {
		t.Fatalf("tools/list returned %d tools", n)
	}
	if text, isErr := toolResult(t, r["3"]); isErr || !strings.Contains(text, `"lane"`) {
		t.Fatalf("route: isError=%v text=%s", isErr, text)
	}
	if _, isErr := toolResult(t, r["4"]); isErr {
		t.Fatal("memory_add failed")
	}
	if text, isErr := toolResult(t, r["5"]); isErr || !strings.Contains(text, "-retry uses jittered backoff") {
		t.Fatalf("memory_search: isError=%v text=%s", isErr, text)
	}
	if text, isErr := toolResult(t, r["6"]); !isErr || !strings.Contains(text, "exit code") {
		t.Fatalf("brief without task: isError=%v text=%s", isErr, text)
	}
	for id, code := range map[string]float64{"7": -32602, "8": -32601, "null": -32700} {
		if got := r[id]["error"].(map[string]any)["code"]; got != code {
			t.Fatalf("id %s: code=%v want %v", id, got, code)
		}
	}
}
