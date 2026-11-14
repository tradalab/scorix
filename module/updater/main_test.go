package updater

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "scorix-updater-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("TMPDIR", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
