package updater

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestARelaunchedAppImageDoesNotKeepTheOldMountAlive(t *testing.T) {
	var keepalive [2]int
	if err := syscall.Pipe(keepalive[:]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Close(keepalive[0])
		_ = syscall.Close(keepalive[1])
	})
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	fd := strconv.Itoa(keepalive[1])
	cmd, err := startAfterExit(gone.Process.Pid, "sh", "-c", "test ! -e /proc/self/fd/"+fd)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the relaunched image inherited fd %s, the old runtime's keepalive pipe, so the old FUSE mount outlives the update: %v", fd, err)
	}
}

func TestSwappingTheAppImageLeavesNoBackupAndNoDownloadDir(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "S3Hub.AppImage")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	download, err := os.MkdirTemp(root, "scorix-update-")
	if err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(download, "installer.AppImage")
	if err := os.WriteFile(newPath, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := swapAppImage(newPath, target); err != nil {
		t.Fatal(err)
	}

	if got, _ := os.ReadFile(target); !bytes.Equal(got, []byte("new")) {
		t.Fatalf("target holds %q, want the new image", got)
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("installed image is not executable: %v %v", fi, err)
	}
	if _, err := os.Stat(target + ".old"); !os.IsNotExist(err) {
		t.Fatalf("backup left behind: %v", err)
	}
	if _, err := os.Stat(download); !os.IsNotExist(err) {
		t.Fatalf("the download dir outlives the update, one image-sized copy per update in TMPDIR: %v", err)
	}
}
