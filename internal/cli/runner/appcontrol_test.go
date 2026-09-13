package runner

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tradalab/scorix/internal/devctl"
)

// fakeApp answers one control request per connection, the way app.devControl does.
func fakeApp(t *testing.T, token string, reply func(op string, args map[string]any) (any, string)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var req struct {
					Token string         `json:"token"`
					Op    string         `json:"op"`
					Args  map[string]any `json:"args"`
				}
				if json.NewDecoder(conn).Decode(&req) != nil {
					return
				}
				out := map[string]any{}
				if req.Token != token {
					out["ok"], out["error"], out["code"] = false, "unauthorized", "denied"
				} else if data, errMsg := reply(req.Op, req.Args); errMsg != "" {
					out["ok"], out["error"] = false, errMsg
				} else {
					raw, _ := json.Marshal(data)
					out["ok"], out["data"] = true, json.RawMessage(raw)
				}
				_ = json.NewEncoder(conn).Encode(out)
			}()
		}
	}()
	return ln.Addr().String()
}

func writeControlFile(t *testing.T, addr, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "devctl.json")
	b, _ := json.Marshal(map[string]any{"addr": addr, "token": token, "pid": 4242})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAppControlRoundTrip(t *testing.T) {
	var gotOp string
	var gotArgs map[string]any
	addr := fakeApp(t, "tok", func(op string, args map[string]any) (any, string) {
		gotOp, gotArgs = op, args
		return map[string]any{"count": 2}, ""
	})
	path := writeControlFile(t, addr, "tok")

	res, err := appControlAt(context.Background(), path, AppControlOptions{
		Op:   "dom",
		Args: map[string]any{"selector": "button"},
	})
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if gotOp != "dom" || gotArgs["selector"] != "button" {
		t.Fatalf("the app saw op=%q args=%v", gotOp, gotArgs)
	}
	if res.PID != 4242 {
		t.Errorf("pid = %d, want the one from the control file", res.PID)
	}
	if !strings.Contains(string(res.Data), `"count":2`) {
		t.Errorf("data = %s", res.Data)
	}
}

func TestAppControlSurfacesTheAppsRefusal(t *testing.T) {
	addr := fakeApp(t, "tok", func(string, map[string]any) (any, string) { return nil, "no element matches #nope" })
	path := writeControlFile(t, addr, "tok")

	_, err := appControlAt(context.Background(), path, AppControlOptions{Op: "input"})
	if err == nil || !strings.Contains(err.Error(), "no element matches") {
		t.Fatalf("the app's own error was swallowed: %v", err)
	}
}

func TestAppControlMissingFileNamesWhereItLooked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devctl.json")
	_, err := appControlAt(context.Background(), path, AppControlOptions{Op: "status"})
	if err == nil {
		t.Fatal("a missing control file read as success")
	}
	// The message has to carry both halves, or the caller cannot tell "no app is
	// running" from "the tool is broken".
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), devctl.Env) {
		t.Errorf("error does not say where it looked or how to fix it: %v", err)
	}
}

// The file survives a crash, so a refused dial is the common case.
func TestAppControlStaleFileSaysSo(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing is listening there any more

	path := writeControlFile(t, addr, "tok")
	_, err = appControlAt(context.Background(), path, AppControlOptions{Op: "status", Timeout: 2 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("a dead port did not read as stale: %v", err)
	}
}

// The app resolves its data dir from the `app:` block, so the CLI must read the
// same key: the top-level `name:` is a different key that starts out identical.
func TestAppControlPathFollowsTheAppBlock(t *testing.T) {
	root := t.TempDir()
	manifest := "name: cli-recipe-name\napp:\n  name: runtime-app-name\n  identifier: com.example.x\n"
	if err := os.WriteFile(filepath.Join(root, "scorix.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	path := AppControlPath(root)
	if !strings.Contains(path, "runtime-app-name") {
		t.Errorf("path %q ignores app.name", path)
	}
	if strings.Contains(path, "cli-recipe-name") {
		t.Errorf("path %q was built from the top-level name:, which the app never reads", path)
	}
}

func TestAppControlPathFallsBackToTheIdentifier(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "scorix.yaml"), []byte("app:\n  identifier: com.example.only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if path := AppControlPath(root); !strings.Contains(path, "com.example.only") {
		t.Errorf("path %q drops the identifier fallback the app uses", path)
	}
}
