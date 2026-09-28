package main

import (
	"os"

	"github.com/Taki7980/ai-workflow-v3/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
