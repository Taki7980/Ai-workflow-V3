// Command fakeprovider is a test retrieval provider: it echoes one item per
// request plus one item with an escaping path that must be confined.
package main

import (
	"encoding/json"
	"io"
	"os"
)

func main() {
	in, _ := io.ReadAll(os.Stdin)
	var req struct {
		Query  string `json:"query"`
		Intent string `json:"intent"`
	}
	_ = json.Unmarshal(in, &req)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"items": []map[string]any{
		{"text": "external evidence for " + req.Query + " (" + req.Intent + ")", "score": 0.8, "path": "pay.go", "line": 3},
		{"text": "escape attempt", "score": 0.2, "path": "../../outside.txt"},
	}})
}
