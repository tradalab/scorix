// Package devctl is the contract between an app that opens its dev control
// socket and the CLI that drives it. The two sides cannot import each other, and
// a second copy of a wire format drifts in silence: the symptom would be a CLI
// reporting "no app" about an app that is up.
package devctl

import "strings"

// Off unless this says otherwise: everything the socket offers is code execution
// in the user's session.
const Env = "SCORIX_DEV_CONTROL"

// FileName is published under the app's data dir, never inside the project:
// `scorix init` emits no .gitignore, so a file in the project is a token waiting
// to be committed.
const FileName = "devctl.json"

type File struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

// Only the explicit yes-words count, so a typo or a leftover value fails closed
// instead of opening a door.
func On(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}
