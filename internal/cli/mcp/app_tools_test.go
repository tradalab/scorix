package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tradalab/scorix/internal/devctl"
)

func toolNames(t *testing.T, s *Server) []string {
	t.Helper()
	got := converse(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var names []string
	for _, entry := range result(t, byID(t, got, "1"))["tools"].([]any) {
		names = append(names, entry.(map[string]any)["name"].(string))
	}
	return names
}

// Without this the five app tools are on the surface and permanently useless:
// nothing else in the chain turns the socket on.
func TestDevJobHandsTheAppItsControlSwitch(t *testing.T) {
	env := strings.Join(devChildEnv(), "\n")
	if !strings.Contains(env, devctl.Env+"=1") {
		t.Error("the dev child is started without SCORIX_DEV_CONTROL, so scorix_app_* can never reach it")
	}
	if len(devChildEnv()) < 2 {
		t.Error("the parent environment was dropped, so the child loses PATH and everything else")
	}
}

func TestAppToolsAreOnTheSurface(t *testing.T) {
	names := toolNames(t, NewServer("test"))
	for _, want := range []string{"scorix_app_status", "scorix_app_eval", "scorix_app_dom", "scorix_app_input", "scorix_app_call"} {
		if !strings.Contains(strings.Join(names, " "), want) {
			t.Errorf("%s is missing from tools/list: %v", want, names)
		}
	}
}

// End to end through the real server: no app is running, so the answer has to be
// the envelope that says where it looked and how to turn the socket on. Getting
// this wrong reads as "the tool is broken" to whoever called it.
func TestAppToolWithNoRunningAppExplainsItself(t *testing.T) {
	dir, _ := json.Marshal(t.TempDir())
	got := converse(t, NewServer("test"), fmt.Sprintf(
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"scorix_app_status","arguments":{"dir":%s}}}`, dir))

	raw, err := json.Marshal(result(t, byID(t, got, "7")))
	if err != nil {
		t.Fatal(err)
	}
	answer := string(raw)
	if !strings.Contains(answer, "SCORIX_DEV_CONTROL") {
		t.Errorf("the answer does not say how to open the socket: %s", answer)
	}
	if !strings.Contains(answer, "devctl.json") {
		t.Errorf("the answer does not say which file it looked for: %s", answer)
	}
}
