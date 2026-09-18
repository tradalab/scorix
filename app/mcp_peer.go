package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// mcpPeer is the program the user approves: a path, not the name a client sends.
type mcpPeer struct {
	PID  int
	Path string
}

type mcpProcInfo struct {
	Path  string
	PPID  int
	Start uint64 // comparable only with another Start on the same machine
}

// Test seams: the real ones name whatever ran `go test`.
var (
	mcpIdentify = identifyMCPClient
	mcpProc     = readMCPProc
)

// The token is readable by every program the user runs; only the OS says which connected.
func identifyMCPClient(conn net.Conn) (mcpPeer, error) {
	pid, err := mcpPeerPID(conn)
	if err != nil {
		return mcpPeer{}, fmt.Errorf("the OS did not say which process connected: %w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return mcpPeer{}, err
	}
	return mcpClientOf(pid, self)
}

// Enough for a client that goes through a shell or two.
const mcpMaxHops = 6

// Walks up past the relay, which runs this app's binary, and past shells.
func mcpClientOf(pid int, self string) (mcpPeer, error) {
	var childStart uint64
	for hop := range mcpMaxHops {
		p, err := mcpProc(pid)
		if err != nil {
			if hop > 0 {
				return mcpPeer{}, fmt.Errorf("the program that started the relay has already exited: %w", err)
			}
			return mcpPeer{}, err
		}
		// A pid freed by a parent that exited must not inherit its approval.
		if hop > 0 && p.Start > childStart {
			return mcpPeer{}, errors.New("the program that started the relay has already exited")
		}
		passThrough := (hop == 0 && isOwnBinary(p.Path, self)) || (hop > 0 && isShell(p.Path))
		if !passThrough {
			return mcpPeer{PID: pid, Path: p.Path}, nil
		}
		if p.PPID <= 0 || p.PPID == pid {
			return mcpPeer{}, errors.New("the relay has no program above it")
		}
		childStart, pid = p.Start, p.PPID
	}
	return mcpPeer{}, fmt.Errorf("more than %d processes between the relay and its client", mcpMaxHops)
}

func isOwnBinary(path, self string) bool {
	if samePath(path, self) {
		return true
	}
	a, errA := os.Stat(path)
	b, errB := os.Stat(self)
	if errA != nil || errB != nil {
		return false
	}
	if os.SameFile(a, b) {
		return true
	}
	// An AppImage mounts itself per process: same bytes, different path.
	if a.Size() != b.Size() {
		return false
	}
	da, errA := fileDigest(path)
	db, errB := fileDigest(self)
	return errA == nil && errB == nil && da == db
}

func fileDigest(path string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return sum, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

var mcpShells = []string{"sh", "bash", "zsh", "dash", "fish", "ksh", "env", "cmd", "powershell", "pwsh"}

func isShell(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	base = strings.TrimSuffix(base, ".exe")
	for _, s := range mcpShells {
		if base == s {
			return true
		}
	}
	return false
}

func samePath(a, b string) bool { return mcpPathKey(a) == mcpPathKey(b) }

func mcpPathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

// Random, not per pid: two instances must not share one, and in a shared temp dir
// a predictable name is one another user can take first.
func mcpSocketPath(dir string) string {
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	name := "mcp-" + hex.EncodeToString(raw) + ".sock"
	// sun_path holds 104 bytes on macOS; the temp dir is the short place left.
	if p := filepath.Join(dir, name); len(p) < 104 {
		return p
	}
	return filepath.Join(os.TempDir(), "scorix-"+name)
}
