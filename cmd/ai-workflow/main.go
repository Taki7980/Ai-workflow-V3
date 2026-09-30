package main

import (
	"os"

	"github.com/Taki7980/ai-workflow-v3/internal/cli"
)

// main runs the ai-workflow CLI, exiting the process with the resulting status code.
func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
