package runner

import (
	"slices"
	"testing"
)

func TestLinuxdeployWritesTheAppImageIntoTheProjectEvenWhenLaunchedFromAnotherAppImage(t *testing.T) {
	inherited := []string{
		"PATH=/usr/bin",
		"OWD=/home/u",
		"APPIMAGE=/usr/local/bin/ghostty",
		"APPDIR=/tmp/.mount_ghostX",
		"ARGV0=ghostty",
	}
	env := linuxdeployEnv(inherited, "/src/app", "1.2.3")
	for _, want := range []string{"PATH=/usr/bin", "OWD=/src/app", "VERSION=1.2.3"} {
		if !slices.Contains(env, want) {
			t.Errorf("env lacks %q: %v", want, env)
		}
	}
	for _, kv := range env {
		switch kv {
		case "OWD=/home/u", "APPIMAGE=/usr/local/bin/ghostty", "APPDIR=/tmp/.mount_ghostX", "ARGV0=ghostty":
			t.Errorf("env still carries the terminal's %q, so the appimage plugin writes the image there", kv)
		}
	}
}
