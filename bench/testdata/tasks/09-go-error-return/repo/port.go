package netcfg

import "strconv"

// ParsePort reads a TCP port number.
func ParsePort(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
