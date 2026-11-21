// Command web serves the dashboard.
package main

import (
	"log"
	"net/http"

	"web/internal/config"
)

func main() {
	addr := config.Load().Addr()
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
