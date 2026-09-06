package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

// runCLI drives the real cobra tree, so it covers the wiring a unit test of
// jsonOut cannot: that each command actually goes through jsonCommand.
func runCLI(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	real := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	// The tree is built once in init() and cobra keeps parsed flags on it, so a
	// --json from an earlier run leaks into this one. Anything driving the tree
	// in-process (scorix mcp) has to reset like this or rebuild per call.
	resetJSONFlag(rootCmd)
	rootCmd.SetArgs(args)
	err = rootCmd.ExecuteContext(context.Background())

	os.Stdout = real
	w.Close()
	return <-done, err
}

func resetJSONFlag(c *cobra.Command) {
	if c.Flags().Lookup("json") != nil {
		_ = c.Flags().Set("json", "false")
	}
	for _, sub := range c.Commands() {
		resetJSONFlag(sub)
	}
}

// Every --json command must put exactly one document on stdout - success or
// failure. A stray progress line here is the whole contract breaking.
func TestJSONCommandsEmitExactlyOneDocument(t *testing.T) {
	empty := t.TempDir() // no scorix.yaml: the commands that need one will fail, which is fine

	cases := []struct {
		name string
		args []string
	}{
		{"doctor", []string{"doctor", "--json"}},
		{"build", []string{"build", "--json", "-d", empty}},
		{"generate proto", []string{"generate", "proto", "--json", "-d", empty, "--check"}},
		{"generate model", []string{"generate", "model", "--json", "-d", empty, "--check"}},
		{"package", []string{"package", "--json", "-d", empty}},
		{"appcast", []string{"appcast", "--json", "-d", empty}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, _ := runCLI(t, c.args...)
			dec := json.NewDecoder(strings.NewReader(out))
			var doc map[string]any
			if err := dec.Decode(&doc); err != nil {
				t.Fatalf("stdout is not a JSON document: %v\n%q", err, out)
			}
			if err := dec.Decode(&map[string]any{}); err != io.EOF {
				t.Fatalf("stdout carried more than one document: %q", out)
			}
			if doc["command"] == "" || doc["command"] == nil {
				t.Fatalf("envelope has no command: %v", doc)
			}
			if _, ok := doc["ok"].(bool); !ok {
				t.Fatalf("envelope has no ok: %v", doc)
			}
			if _, ok := doc["exit"].(float64); !ok {
				t.Fatalf("envelope has no exit: %v", doc)
			}
		})
	}
}

// Without --json nothing may appear on stdout that looks like a result
// document; the human mode must stay exactly as it was.
func TestWithoutJSONNoDocument(t *testing.T) {
	out, _ := runCLI(t, "build", "-d", filepath.Join(t.TempDir(), "absent"))
	var doc map[string]any
	if json.Unmarshal([]byte(out), &doc) == nil {
		t.Fatalf("human mode emitted a JSON document: %q", out)
	}
}

// A bad flag must be distinguishable from a run that failed, or an agent
// retries a call that can never work.
func TestUnknownFlagIsExitUsage(t *testing.T) {
	_, err := runCLI(t, "doctor", "--nonsense")
	if err == nil {
		t.Fatal("an unknown flag must fail")
	}
	var ee *runner.ExitError
	if !errors.As(err, &ee) || ee.Code != runner.ExitUsage {
		t.Fatalf("want exit %d, got %#v", runner.ExitUsage, err)
	}
}
