package conf

import "strings"

// Config is the parsed configuration.
type Config map[string]string

// ParseConfig reads key=value lines.
func ParseConfig(s string) Config {
	c := Config{}
	for _, line := range strings.Split(s, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			c[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return c
}
