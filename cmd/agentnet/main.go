//go:build unix

package main

import (
	"os"

	"github.com/karenrebecag/agentnet/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
