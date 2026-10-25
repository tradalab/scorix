package hostapi

import _ "embed"

//go:generate go run ./gen

// Proto is host.proto, which the CLI reads to know which plugin names are the host's own.
//
//go:embed host.proto
var Proto string
