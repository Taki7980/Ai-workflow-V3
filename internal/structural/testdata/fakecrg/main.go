// Command fakecrg stands in for code-review-graph in tests. It replays
// fixture JSON from CRG_FAKE_FIXTURES with {{ROOT}} replaced by CRG_REPO_ROOT.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	args := os.Args[1:]
	root := filepath.ToSlash(os.Getenv("CRG_REPO_ROOT"))
	if log := os.Getenv("CRG_FAKE_LOG"); log != "" {
		f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%s\t%s\n", root, strings.Join(args, " "))
			f.Close()
		}
	}
	switch os.Getenv("CRG_FAKE_MODE") {
	case "fail":
		os.Exit(3)
	case "hang":
		time.Sleep(30 * time.Second)
	case "garbage":
		fmt.Println("not json")
		return
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	name := args[0]
	switch name {
	case "--version":
		fmt.Println("code-review-graph 2.3.8")
		return
	case "build", "update":
		if os.Getenv("CRG_FAKE_MODE") == "nodb" {
			return
		}
		dir := os.Getenv("CRG_DATA_DIR")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			os.Exit(4)
		}
		if err := os.WriteFile(filepath.Join(dir, "graph.db"), []byte("fake "+name), 0o644); err != nil {
			os.Exit(4)
		}
		return
	case "query":
		if len(args) < 3 {
			os.Exit(2)
		}
		name = args[1]
		if os.Getenv("CRG_FAKE_AMBIGUOUS") == "1" && !strings.Contains(args[2], "::") {
			name = "ambiguous"
		}
	}
	b, err := os.ReadFile(filepath.Join(os.Getenv("CRG_FAKE_FIXTURES"), name+".json"))
	if err != nil {
		b, _ = os.ReadFile(filepath.Join(os.Getenv("CRG_FAKE_FIXTURES"), "not_found.json"))
	}
	fmt.Print(strings.ReplaceAll(string(b), "{{ROOT}}", root))
}
