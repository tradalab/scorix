package runner

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleChangelog = "# Changelog\r\n\r\n---\r\n\r\n## [1.18.1]\r\n\r\n### Chores\r\n\r\n- **ci:** macos-15\r\n\r\n---\r\n\r\n## [1.18.0]\r\n\r\n- older\r\n"

func TestChangelogSectionIsTheBodyUnderTheVersionHeadingWithoutTheRuleBelowIt(t *testing.T) {
	for _, c := range []struct{ changelog, version, want string }{
		{sampleChangelog, "1.18.1", "### Chores\n\n- **ci:** macos-15"},
		{sampleChangelog, "v1.18.0", "- older"},
		{"## v2.0.0 - 2026-11-14\nbig\n## v1.0.0\nsmall\n", "2.0.0", "big"},
		{"## 3.1.0\n\nplain\n", "3.1.0", "plain"},
		{sampleChangelog, "1.18", ""},
		{"## [1.1.0]\n\nRun:\n\n```md\n## not a heading\n```\n\n## [1.0.0]\nold\n", "1.1.0", "Run:\n\n```md\n## not a heading\n```"},
		{"## [1.0.0]\n\n- first\n\n[1.0.0]: https://example.com/compare/v0.9.0...v1.0.0\n[Unreleased]: https://example.com/compare/v1.0.0...HEAD\n", "1.0.0", "- first"},
		{sampleChangelog, "9.9.9", ""},
		{"## [1.2.0](https://example.com/compare/v1.1.0...v1.2.0) (2026-11-14)\n\n### Features\n\n- new\n\n### [1.1.1](https://example.com/compare/v1.1.0...v1.1.1) (2026-11-01)\n\n- fix\n", "1.2.0", "### Features\n\n- new"},
		{"## [1.2.0](https://example.com/c) (2026-11-14)\n\n- new\n\n### [1.1.1](https://example.com/c) (2026-11-01)\n\n### Bug Fixes\n\n- fix\n", "1.1.1", "### Bug Fixes\n\n- fix"},
		{"## [1.0.0]\n\nSee [the guide][mig].\n\n[mig]: https://example.com/mig\n[1.0.0]: https://example.com/compare\n", "1.0.0", "See [the guide][mig].\n\n[mig]: https://example.com/mig"},
	} {
		if got := changelogSection(c.changelog, c.version); got != c.want {
			t.Errorf("version %s: got %q, want %q", c.version, got, c.want)
		}
	}
}

func TestReleaseNotesFromScorixYamlWinOverTheChangelog(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CHANGELOG.md"), []byte(sampleChangelog), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := releaseNotes(root, &UpdateConfig{Notes: " Hand written. "}, "1.18.1")
	if err != nil || got != "Hand written." {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = releaseNotes(root, &UpdateConfig{}, "1.18.1")
	if err != nil || got != "### Chores\n\n- **ci:** macos-15" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestReleaseNotesWithoutAChangelogAreEmptyNotAnError(t *testing.T) {
	got, err := releaseNotes(t.TempDir(), &UpdateConfig{}, "1.0.0")
	if err != nil || got != "" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestReleaseNotesFromAnEarlierRunDoNotOutliveAVersionWithout(t *testing.T) {
	dir := t.TempDir()
	out, err := writeReleaseNotes(dir, "- first")
	if err != nil || out == "" {
		t.Fatalf("write: %q, %v", out, err)
	}
	if out, err = writeReleaseNotes(dir, ""); err != nil || out != "" {
		t.Fatalf("clear: %q, %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, releaseNotesFile)); !os.IsNotExist(err) {
		t.Fatalf("stale %s survived: %v", releaseNotesFile, err)
	}
	if _, err := writeReleaseNotes(dir, ""); err != nil {
		t.Fatalf("clearing twice: %v", err)
	}
}
