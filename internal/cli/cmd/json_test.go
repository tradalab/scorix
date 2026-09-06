package cmd

import (
	"fmt"
	"io"
	"os"
	"testing"
)

// The whole --json contract rests on this swap, so it gets its own test: a
// progress line printed while it is active must not reach stdout, or every
// parser downstream breaks on the first build that prints something new.
func TestJSONOutKeepsStdoutClean(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	real := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = real }()

	out, restore := jsonOut(true)
	if out != w {
		t.Fatal("the runner must be handed the stdout that was live when --json started")
	}
	fmt.Println("progress noise")
	fmt.Fprintf(os.Stdout, "subprocess noise\n")
	fmt.Fprintln(out, `{"ok":true}`)
	restore()

	if os.Stdout != w {
		t.Fatal("restore did not put stdout back")
	}
	w.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "{\"ok\":true}\n" {
		t.Fatalf("stdout carried more than the document: %q", got)
	}
}

func TestJSONOutOffLeavesStdoutAlone(t *testing.T) {
	before := os.Stdout
	out, restore := jsonOut(false)
	defer restore()
	if out != nil {
		t.Fatal("no --json means no JSON writer, so the runner stays in human mode")
	}
	if os.Stdout != before {
		t.Fatal("human mode must not touch stdout")
	}
}
