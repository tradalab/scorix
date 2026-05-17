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

func writeGeneratedFile(f generatedFile) (string, error) {
	_, statErr := os.Stat(f.Path)
	exists := statErr == nil
	if exists && !f.Force {
		return "skipped", nil
	}
	if statErr != nil && !os.IsNotExist(statErr) {
		return "", statErr
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
		return "", err
	}
	if err := tpl.Execute(&buf, f.Data); err != nil {
		return "", err
	}

	content := buf.Bytes()
	if f.Go {
		formatted, err := format.Source(content)
		if err != nil {
			return "", fmt.Errorf("format %s: %w\n%s", f.Path, err, content)
		}
		content = formatted
	}

	if err := os.MkdirAll(filepath.Dir(f.Path), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(f.Path, content, 0644); err != nil {
		return "", err
	}
	if exists {
		return "updated", nil
	}
	return "created", nil
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
