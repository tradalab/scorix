package runner

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// A drive letter would otherwise be eaten as the filename on Windows, so the
// optional "X:" prefix is matched before the path.
var goDiagRe = regexp.MustCompile(`^((?:[A-Za-z]:)?[^:]+):(\d+)(?::(\d+))?: (.*)$`)

// The compiler text is printed as it is read: stdout belongs to the JSON
// document, so this is the only copy a human gets.
func collectBuildDiagnostics(bc *BuildContext, r io.Reader) {
	dec := json.NewDecoder(r)
	for {
		var ev struct {
			ImportPath string
			Action     string
			Output     string
		}
		if err := dec.Decode(&ev); err != nil {
			if err != io.EOF {
				// Whatever the toolchain said next is not ours to swallow: this is
				// the only copy the human gets, stdout being the JSON document.
				io.Copy(os.Stdout, io.MultiReader(dec.Buffered(), r))
			}
			return
		}
		if ev.Output == "" {
			continue
		}
		fmt.Print(ev.Output)
		for _, line := range strings.Split(strings.TrimRight(ev.Output, "\n"), "\n") {
			// "# package/path" is a header for the lines under it, not a message.
			if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
				continue
			}
			*bc.diag = append(*bc.diag, parseGoDiagnostic(ev.ImportPath, line))
		}
	}
}

func parseGoDiagnostic(pkg, line string) BuildDiagnostic {
	d := BuildDiagnostic{Package: pkg, Message: strings.TrimSpace(line)}
	m := goDiagRe.FindStringSubmatch(line)
	if m == nil {
		return d
	}
	// Same shape as the drift payload: forward slashes, no leading "./".
	file := filepath.ToSlash(m[1])
	file = strings.TrimPrefix(file, "./")
	d.File, d.Message = file, m[4]
	d.Line, _ = strconv.Atoi(m[2])
	d.Col, _ = strconv.Atoi(m[3])
	return d
}
