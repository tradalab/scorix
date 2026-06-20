package runner

import (
	"bytes"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	scorix_template "github.com/tradalab/scorix/internal/cli/template"
)

// stagedFile is a rendered generatedFile held in memory so a batch can render
// everything before committing any (all-or-nothing).
type stagedFile struct {
	Path       string
	Content    []byte
	Action     string // "created" | "updated" | "skipped"
	NeedsWrite bool
}

// renderGeneratedFile renders into memory without touching the filesystem, so an
// error aborts before any write.
func renderGeneratedFile(f generatedFile) (stagedFile, error) {
	_, statErr := os.Stat(f.Path)
	exists := statErr == nil
	if exists && !f.Force {
		return stagedFile{Path: f.Path, Action: "skipped"}, nil
	}
	if statErr != nil && !os.IsNotExist(statErr) {
		return stagedFile{}, statErr
	}

	var buf bytes.Buffer
	tpl, err := template.New(filepath.Base(f.Path)).Funcs(template.FuncMap{
		"lower": strings.ToLower,
		"lowerFirst": func(s string) string {
			if s == "" {
				return s
			}
			return strings.ToLower(s[:1]) + s[1:]
		},
	}).Parse(f.Template)
	if err != nil {
		return stagedFile{}, err
	}
	if err := tpl.Execute(&buf, f.Data); err != nil {
		return stagedFile{}, err
	}

	content := buf.Bytes()
	if f.Go {
		formatted, err := format.Source(content)
		if err != nil {
			return stagedFile{}, fmt.Errorf("format %s: %w\n%s", f.Path, err, content)
		}
		content = formatted
	}

	action := "created"
	if exists {
		action = "updated"
	}
	return stagedFile{Path: f.Path, Content: content, Action: action, NeedsWrite: true}, nil
}

// normalizeNewlines strips CR so a git autocrlf checkout on Windows can't
// produce false drift against the LF bytes the generator renders.
func normalizeNewlines(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("\r"), nil)
}

func driftOf(s stagedFile) (string, error) {
	if !s.NeedsWrite {
		return "", nil
	}
	disk, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	if bytes.Equal(normalizeNewlines(disk), normalizeNewlines(s.Content)) {
		return "", nil
	}
	return "out of date", nil
}

func reportDrift(root, regenCmd string, drifted []string) error {
	if len(drifted) == 0 {
		fmt.Println("==> Check passed: generated code is in sync.")
		return nil
	}
	for _, d := range drifted {
		fmt.Printf("      drift: %s\n", d)
	}
	return fmt.Errorf("generated code is out of sync with its sources (%d file(s)) — run `%s` and commit the result", len(drifted), regenCmd)
}

func driftLabel(root, path, reason string) string {
	if rel, err := filepath.Rel(root, path); err == nil {
		path = filepath.ToSlash(rel)
	}
	return path + " (" + reason + ")"
}

func commitStagedFile(s stagedFile) error {
	if !s.NeedsWrite {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0755); err != nil {
		return err
	}
	return os.WriteFile(s.Path, s.Content, 0644)
}

// writeGeneratedFile renders and immediately writes one file — for callers that
// emit independently, not as an all-or-nothing batch.
func writeGeneratedFile(f generatedFile) (string, error) {
	staged, err := renderGeneratedFile(f)
	if err != nil {
		return "", err
	}
	if err := commitStagedFile(staged); err != nil {
		return "", err
	}
	return staged.Action, nil
}

func writeTemplateFS(srcDir, destDir string, data any) error {
	return fs.WalkDir(scorix_template.StaticFS, srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == srcDir {
			return nil
		}
		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(destDir, relPath)
		if d.IsDir() {
			return os.MkdirAll(targetPath, 0755)
		}
		// Strip the .tpl suffix used to keep template files out of `go build`.
		targetPath = strings.TrimSuffix(targetPath, ".tpl")

		tplContent, err := scorix_template.StaticFS.ReadFile(path)
		if err != nil {
			return err
		}

		ext := strings.ToLower(filepath.Ext(targetPath))
		isBinary := ext == ".ico" || ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif"

		var action string
		if isBinary {
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return err
			}
			if err := os.WriteFile(targetPath, tplContent, 0644); err != nil {
				return err
			}
			action = "created"
		} else {
			action, err = writeGeneratedFile(generatedFile{
				Path:     targetPath,
				Template: string(tplContent),
				Data:     data,
				Go:       strings.HasSuffix(targetPath, ".go"),
			})
			if err != nil {
				return err
			}
		}
		fmt.Printf("%s: %s\n", action, targetPath)
		return nil
	})
}

func readModulePath(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("module path not found in %s", path)
}
