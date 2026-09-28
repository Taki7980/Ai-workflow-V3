package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/Taki7980/ai-workflow-v3/internal/compat"
)

func main() {
	casesPath := flag.String("cases", "compat/cases.json", "compatibility input cases")
	fixturePath := flag.String("fixtures", "compat/fixtures/v2-contracts.json", "frozen V2 outputs")
	lockPath := flag.String("lock", "compat/v2.lock", "pinned V2 commit")
	flag.Parse()

	report, err := compat.Run(*casesPath, *fixturePath, *lockPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(b))
	if !report.OK {
		os.Exit(1)
	}
}
