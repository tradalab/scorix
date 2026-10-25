// Command gen rewrites the built-in modules' TS fragments; go generate runs it in internal/modulets.
package main

import (
	"log"

	"github.com/tradalab/scorix/internal/modulets"
)

func main() {
	if err := modulets.Generate("../..", false); err != nil {
		log.Fatal(err)
	}
}
