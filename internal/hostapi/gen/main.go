// Command gen rewrites hostapi.go and HostPlugins.kt from host.proto; go generate runs it here.
package main

import (
	"log"

	"github.com/tradalab/scorix/internal/cli/runner"
)

func main() {
	if err := runner.GenerateHostAPI("../..", false); err != nil {
		log.Fatal(err)
	}
}
