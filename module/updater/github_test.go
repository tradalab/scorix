package updater

import "testing"

func TestAssetMatchesPlatform(t *testing.T) {
	cases := []struct {
		name     string
		asset    string
		platform string
		want     bool
	}{
		// Windows
		{"win msi", "RedisHub-1.11.0-windows-amd64.msi", "windows-amd64", true},
		{"win sig", "RedisHub-1.11.0-windows-amd64.msi.sig", "windows-amd64", true},
		{"win vs linux", "RedisHub-1.11.0-windows-amd64.msi", "linux-amd64", false},

		// Linux
		{"linux appimage", "RedisHub-1.11.0-linux-amd64.AppImage", "linux-amd64", true},
		{"linux x86_64 alias", "RedisHub-1.11.0-linux-x86_64.AppImage", "linux-amd64", true},
		{"linux vs darwin", "RedisHub-1.11.0-linux-amd64.AppImage", "darwin-amd64", false},

		// macOS — the packager names dmgs "macos", updater key is "darwin"
		{"macos universal -> darwin-arm64", "RedisHub-1.11.0-macos-universal.dmg", "darwin-arm64", true},
		{"macos universal -> darwin-amd64", "RedisHub-1.11.0-macos-universal.dmg", "darwin-amd64", true},
		{"macos universal sig", "RedisHub-1.11.0-macos-universal.dmg.sig", "darwin-arm64", true},
		{"darwin per-arch exact", "RedisHub-1.11.0-darwin-arm64.dmg", "darwin-arm64", true},
		{"darwin aarch64 alias", "RedisHub-1.11.0-macos-aarch64.dmg", "darwin-arm64", true},
		{"darwin amd64 dmg NOT for arm64 user", "RedisHub-1.11.0-macos-amd64.dmg", "darwin-arm64", false},
		{"macos dmg NOT for windows user", "RedisHub-1.11.0-macos-universal.dmg", "windows-amd64", false},

		// Malformed key
		{"no dash", "whatever.msi", "windows", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := assetMatchesPlatform(c.asset, c.platform); got != c.want {
				t.Errorf("assetMatchesPlatform(%q, %q) = %v, want %v", c.asset, c.platform, got, c.want)
			}
		})
	}
}
