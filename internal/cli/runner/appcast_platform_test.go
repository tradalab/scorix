package runner

import (
	"strings"
	"testing"
)

func TestEveryCollectedInstallerHasAPlatform(t *testing.T) {
	for _, ext := range installerExts {
		for _, name := range []string{"App-1.2.3-windows-amd64", "App-1.2.3-darwin-arm64", "App-1.2.3"} {
			for _, cased := range []string{ext, strings.ToUpper(ext)} {
				if keys := platformKeysForArtifact(name + cased); len(keys) == 0 {
					t.Errorf("%s is collected as an installer but maps to no platform", name+cased)
				}
			}
		}
	}
}
