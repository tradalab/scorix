package deeplink

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, "book.RDM")
	if err := os.WriteFile(doc, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{
		"MyApp://Open/Thing?x=1", // scheme match is case-insensitive, URL kept verbatim
		"other://nope",
		doc,                             // declared ext, exists
		filepath.Join(dir, "ghost.rdm"), // declared ext, missing
		filepath.Join(dir, "notes.txt"), // undeclared ext
		"relative.rdm",                  // relative: would resolve against cwd
		"--flag",
		"",
	}
	urls, files := Classify(args, []string{"myapp"}, []string{"rdm"})
	if len(urls) != 1 || urls[0] != "MyApp://Open/Thing?x=1" {
		t.Fatalf("urls = %v", urls)
	}
	if len(files) != 1 || !strings.EqualFold(files[0], doc) {
		t.Fatalf("files = %v", files)
	}

	if u, f := Classify(args, nil, nil); u != nil || f != nil {
		t.Fatalf("nothing declared must classify nothing, got %v %v", u, f)
	}
}
