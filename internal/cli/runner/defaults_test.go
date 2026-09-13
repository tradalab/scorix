package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The CLI keeps its defaults in cobra flags (--proto idl/app.proto, --schema
// idl/schema.sql), which a caller that is not cobra never sees. scorix mcp
// passed the zero value and generate model read the project root as a file.
func TestZeroValueOptionsUseTheSamePathsAsTheFlags(t *testing.T) {
	// A manifest that names neither path, so the default is the only source.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scorix.yaml"), []byte("name: probe\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		run  func() error
		want string
	}{
		{"generate proto", func() error {
			return GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir})
		}, "idl"},
		{"generate model", func() error {
			return GenerateModel(context.Background(), GenerateModelOptions{Dir: dir})
		}, "idl"},
	}
	for _, c := range cases {
		err := c.run()
		if err == nil {
			t.Fatalf("%s: an empty project should fail", c.name)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s looked for %q, not the flag's default path: %v", c.name, c.want, err)
		}
	}
}
