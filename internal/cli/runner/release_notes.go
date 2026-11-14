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

var (
	linkReference = regexp.MustCompile(`^\[([^\]]+)\]:\s+\S+$`)
	versionLike   = regexp.MustCompile(`^\d+\.\d+`)
)

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
		if heading, ok := releaseHeading(line); !fenced && ok {
			if inside {
				break
			}
			inside = heading == strings.TrimPrefix(version, "v")
			continue
		}
		if inside {
			out = append(out, line)
		}
	}
	for len(out) > 0 {
		last := strings.TrimSpace(out[len(out)-1])
		if last != "" && last != "---" && !unusedReference(last, out[:len(out)-1]) {
			break
		}
		out = out[:len(out)-1]
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func unusedReference(line string, body []string) bool {
	m := linkReference.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	label := strings.ToLower("[" + m[1] + "]")
	for _, l := range body {
		if strings.Contains(strings.ToLower(l), label) && !linkReference.MatchString(strings.TrimSpace(l)) {
			return false
		}
	}
	return true
}

func releaseHeading(line string) (string, bool) {
	var rest string
	switch {
	case strings.HasPrefix(line, "## "):
		rest = line[3:]
	case strings.HasPrefix(line, "### "):
		rest = line[4:]
	default:
		return "", false
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", line[:3] == "## "
	}
	v := strings.TrimPrefix(fields[0], "[")
	if end := strings.IndexByte(v, ']'); end >= 0 {
		v = v[:end]
	}
	v = strings.TrimPrefix(v, "v")
	if !strings.HasPrefix(line, "## ") && !versionLike.MatchString(v) {
		return "", false
	}
	return v, true
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
