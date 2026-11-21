package netcfg

import (
	"fmt"
	"strconv"
)

// ParsePort reads a TCP port number in the range 1-65535.
func ParsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("port %q is not a number", s)
	}
	if n < 1 || n > 65535 {
		return 0, fmt.Errorf("port %d is out of range", n)
	}
	return n, nil
}
