package runner

import "testing"

func TestSignGracefulDegradation(t *testing.T) {
	cfg := &PackageConfig{Sign: &SignConfig{
		Windows: &WindowsSign{Enabled: true, CertEnv: "TEST_WIN_CERT"},
		MacOS:   &MacOSSign{Enabled: true, IdentityEnv: "TEST_MAC_ID"},
	}}

	t.Run("enabled but no creds -> graceful skip", func(t *testing.T) {
		bc := &BuildContext{pkg: cfg}
		if bc.winSign() != nil {
			t.Error("windows: want nil (unsigned) when cert env empty")
		}
		if bc.macSign() != nil {
			t.Error("macos: want nil (unsigned) when identity empty")
		}
	})

	t.Run("enabled with creds -> sign", func(t *testing.T) {
		t.Setenv("TEST_WIN_CERT", "deadbeef")
		t.Setenv("TEST_MAC_ID", "Developer ID Application: Acme (TEAMID)")
		bc := &BuildContext{pkg: cfg}
		if bc.winSign() == nil {
			t.Error("windows: want non-nil when cert env set")
		}
		if bc.macSign() == nil {
			t.Error("macos: want non-nil when identity set")
		}
	})

	t.Run("--sign forces strict (no graceful skip)", func(t *testing.T) {
		bc := &BuildContext{pkg: &PackageConfig{Sign: &SignConfig{
			Windows: &WindowsSign{Enabled: false, CertEnv: "TEST_WIN_CERT"},
		}}, ForceSign: true}
		if bc.winSign() == nil {
			t.Error("windows: --sign must return config (fail loudly) even with no cert")
		}
	})

	t.Run("--skip-sign disables signing", func(t *testing.T) {
		bc := &BuildContext{pkg: cfg, SkipSign: true}
		if bc.winSign() != nil || bc.macSign() != nil {
			t.Error("--skip-sign must disable signing")
		}
	})
}
