package main

import (
	"github.com/frecklelabs/underwraps/internal/boundary"
	"log"
)

func main() {
	if err := boundary.Check("."); err != nil {
		log.Fatal(err)
	}
}
