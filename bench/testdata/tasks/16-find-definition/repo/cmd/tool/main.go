// Command tool prints the configured mode.
package main

import (
	"fmt"

	"finder/internal/app"
	"finder/internal/conf"
)

// parseConfigFlag is not ParseConfig; it only reads a flag.
func parseConfigFlag() string { return conf.Defaults }

func main() { fmt.Println(app.Start(parseConfigFlag())) }
