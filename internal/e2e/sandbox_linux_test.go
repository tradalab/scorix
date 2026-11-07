//go:build linux && !android

package e2e

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tradalab/scorix/internal/driver/webkitgtk"
)

func descendants(root int) map[int]string {
	parent := map[int]int{}
	comm := map[int]string{}
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	for _, d := range dirs {
		pid, _ := strconv.Atoi(filepath.Base(d))
		stat, err := os.ReadFile(d + "/stat")
		if err != nil {
			continue
		}
		s := string(stat)
		end := strings.LastIndexByte(s, ')')
		fields := strings.Fields(s[end+2:])
		parent[pid], _ = strconv.Atoi(fields[1])
		comm[pid] = s[strings.IndexByte(s, '(')+1 : end]
	}
	out := map[int]string{}
	for pid := range comm {
		for p := parent[pid]; p > 1; p = parent[p] {
			if p == root {
				out[pid] = comm[pid]
				break
			}
		}
	}
	return out
}

func TestWebContentRunsInsideTheSandbox(t *testing.T) {
	harness(t)
	if err := webkitgtk.SandboxUsable(); err != nil {
		t.Skipf("this host cannot sandbox web content, so the driver runs it unsandboxed: %v", err)
	}
	procs := descendants(os.Getpid())
	for _, name := range procs {
		if name == "bwrap" {
			return
		}
	}
	t.Fatalf("the page loaded but no bwrap wraps the web process; children: %v", procs)
}
