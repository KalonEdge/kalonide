package app

import "finder/internal/conf"

// Start parses the configuration and returns the mode.
func Start(raw string) string {
	cfg := conf.ParseConfig(raw) // ParseConfig lives in the conf package
	return cfg["mode"]
}
