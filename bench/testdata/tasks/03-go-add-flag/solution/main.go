// Command echoer prints its arguments joined by spaces.
package main

import (
	"flag"
	"fmt"
	"strings"
)

func main() {
	upper := flag.Bool("upper", false, "print in upper case")
	flag.Parse()
	out := strings.Join(flag.Args(), " ")
	if *upper {
		out = strings.ToUpper(out)
	}
	fmt.Println(out)
}
