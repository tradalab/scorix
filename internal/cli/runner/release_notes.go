package runner

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const releaseNotesFile = "RELEASE_NOTES.md"

var linkReference = regexp.MustCompile(`^\[[^\]]+\]:\s+\S+$`)

func releaseNotes(root string, upd *UpdateConfig, version string) (string, error) {
	if notes := strings.TrimSpace(upd.Notes); notes != "" {
		return notes, nil
	}
	data, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return changelogSection(string(data), version), nil
}

func changelogSection(changelog, version string) string {
	var out []string
	inside, fenced := false, false
	for line := range strings.SplitSeq(strings.ReplaceAll(changelog, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		}
		if !fenced && strings.HasPrefix(line, "## ") {
			if inside {
				break
			}
			inside = headingVersion(line) == strings.TrimPrefix(version, "v")
			continue
		}
		if inside {
			out = append(out, line)
		}
	}
	for len(out) > 0 {
		last := strings.TrimSpace(out[len(out)-1])
		if last != "" && last != "---" && !linkReference.MatchString(last) {
			break
		}
		out = out[:len(out)-1]
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func headingVersion(line string) string {
	fields := strings.Fields(strings.TrimPrefix(line, "## "))
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimPrefix(strings.Trim(fields[0], "[]"), "v")
}

func writeReleaseNotes(artDir, notes string) (string, error) {
	out := filepath.Join(artDir, releaseNotesFile)
	if notes == "" {
		if err := os.Remove(out); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		return "", nil
	}
	return out, os.WriteFile(out, []byte(notes+"\n"), 0o644)
}
