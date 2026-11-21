package config

import (
	"fmt"
	"os"
	"strconv"
)

const defaultPort = 8087

// Config holds runtime settings.
type Config struct{ Port int }

// Load reads settings from the environment.
func Load() Config {
	c := Config{Port: defaultPort}
	if v := os.Getenv(portEnv); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			c.Port = p
		}
	}
	return c
}

// Addr is the listen address.
func (c Config) Addr() string { return fmt.Sprintf(":%d", c.Port) }
