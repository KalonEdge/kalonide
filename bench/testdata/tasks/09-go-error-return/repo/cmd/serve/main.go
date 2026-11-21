// Command serve prints the port it would listen on.
package main

import (
	"fmt"
	"os"

	"netcfg"
)

func main() {
	p := "8080"
	if len(os.Args) > 1 {
		p = os.Args[1]
	}
	fmt.Println("listening on", netcfg.ParsePort(p))
}
