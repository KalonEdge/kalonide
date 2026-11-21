// Command echoer prints its arguments joined by spaces.
package main

import (
	"flag"
	"fmt"
	"strings"
)

func main() {
	flag.Parse()
	fmt.Println(strings.Join(flag.Args(), " "))
}
