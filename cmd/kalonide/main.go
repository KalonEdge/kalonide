// Command kalonide is a terminal coding agent for Kalon and any
// OpenAI-compatible model server.
package main

import (
	"os"

	"github.com/KalonEdge/kalonide/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
