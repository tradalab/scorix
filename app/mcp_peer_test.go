package app

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMCPClientIsTheProgramAboveTheRelay(t *testing.T) {
	p := func(parts ...string) string {
		return filepath.Join(append([]string{string(filepath.Separator)}, parts...)...)
	}
	self := p("apps", "demo")
	cases := []struct {
		name    string
		procs   map[int]mcpProcInfo
		want    string
		wantErr string
	}{
		{"started by the client", map[int]mcpProcInfo{
			10: {self, 9, 300}, 9: {p("apps", "claude"), 1, 200},
		}, p("apps", "claude"), ""},
		{"through shells", map[int]mcpProcInfo{
			10: {self, 9, 300}, 9: {p("bin", "sh"), 8, 250}, 8: {p("Windows", "cmd.exe"), 7, 240}, 7: {p("apps", "cursor"), 1, 100},
		}, p("apps", "cursor"), ""},
		{"dialled by a program itself", map[int]mcpProcInfo{
			10: {p("tmp", "script"), 9, 300}, 9: {p("bin", "bash"), 1, 100},
		}, p("tmp", "script"), ""},
		{"parent exited and its pid was reused", map[int]mcpProcInfo{
			10: {self, 9, 300}, 9: {p("apps", "claude"), 1, 400},
		}, "", "exited"},
		{"parent gone", map[int]mcpProcInfo{
			10: {self, 9, 300},
		}, "", "exited"},
	}
	old := mcpProc
	t.Cleanup(func() { mcpProc = old })
	for _, tc := range cases {
		mcpProc = func(pid int) (mcpProcInfo, error) {
			info, ok := tc.procs[pid]
			if !ok {
				return info, errors.New("no such process")
			}
			return info, nil
		}
		got, err := mcpClientOf(10, self)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: got %+v, %v; want an error about %q", tc.name, got, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got.Path != tc.want {
			t.Errorf("%s: got %+v, %v; want %s", tc.name, got, err, tc.want)
		}
	}
}

// An AppImage relay runs the app's bytes from a mount of its own.
func TestMCPOwnBinaryIncludesACopyOfIt(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	self := write("app", "binary-v1")
	if !isOwnBinary(write("mounted-app", "binary-v1"), self) {
		t.Error("the same bytes at another path were not recognised")
	}
	if isOwnBinary(write("other", "binary-v2"), self) {
		t.Error("a different program of the same size passed as the app")
	}
	if isOwnBinary(write("bigger", "binary-v10"), self) {
		t.Error("a different program passed as the app")
	}
}

// Asks the real OS, through a child process whose pid the test knows independently.
func TestMCPPeerIsTheProcessAtTheOtherEnd(t *testing.T) {
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("scorix-peer-%d.sock", os.Getpid()))
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	child := exec.Command(os.Args[0], "-test.run=^TestMCPPeerHelper$")
	child.Env = append(os.Environ(), "SCORIX_MCP_PEER_DIAL="+sock)
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = child.Wait() }()

	_ = ln.(*net.UnixListener).SetDeadline(time.Now().Add(10 * time.Second))
	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("the child never connected: %v", err)
	}
	defer conn.Close()

	pid, err := mcpPeerPID(conn)
	if err != nil || pid != child.Process.Pid {
		t.Fatalf("peer pid = %d, %v; want the child's %d", pid, err, child.Process.Pid)
	}
	info, err := readMCPProc(pid)
	if err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	if !isOwnBinary(info.Path, self) || info.PPID != os.Getpid() {
		t.Errorf("child = %+v; want this test binary, started by pid %d", info, os.Getpid())
	}
	me, err := readMCPProc(os.Getpid())
	if err != nil || me.Start > info.Start {
		t.Errorf("start times: parent %d, child %d (%v); the parent must not start later", me.Start, info.Start, err)
	}
	// The child runs this binary, like a relay, so the client is the process above it.
	peer, err := identifyMCPClient(conn)
	if err != nil || peer.PID != os.Getpid() {
		t.Errorf("client = %+v, %v; want this process (%d)", peer, err, os.Getpid())
	}
}

func TestMCPPeerHelper(t *testing.T) {
	sock := os.Getenv("SCORIX_MCP_PEER_DIAL")
	if sock == "" {
		return
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		os.Exit(2)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = conn.Close()
	os.Exit(0)
}
