// M1 supplies a persistence smoke command; HTTP, OIDC and MCP arrive in M2.
package main

import (
	"flag"
	"github.com/frecklelabs/underwraps/internal/data"
	"log"
	"time"
)

func main() {
	path := flag.String("database", "", "private SQLite file (required)")
	zone := flag.String("timezone", "", "IANA calendar timezone (required)")
	flag.Parse()
	s, err := data.Open(data.Config{Path: *path, Timezone: *zone, Clock: time.Now})
	if err != nil {
		log.Fatal(err)
	}
	if err = s.Close(); err != nil {
		log.Fatal(err)
	}
	log.Print("database foundation ready; no HTTP or MCP listener in M1")
}
