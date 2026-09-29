package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/Taki7980/ai-workflow-v3/internal/eval"
)

func main() {
	fixturePath := flag.String("fixture", "benchmarks/retrieval/baseline.json", "retrieval benchmark fixture")
	enforce := flag.Bool("enforce", false, "exit non-zero when configured thresholds are violated")
	flag.Parse()

	fixture, err := eval.LoadFixture(*fixturePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	report := eval.Run(fixture)
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Println(string(b))
	if *enforce && !report.Pass {
		os.Exit(1)
	}
}
