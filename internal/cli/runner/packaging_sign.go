package runner

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// SignConfig is the optional `package.sign` block. Secrets are never stored
// here — fields ending in *_env name the environment variable that carries the
// secret at build time.
type SignConfig struct {
	Windows *WindowsSign `yaml:"windows"`
	MacOS   *MacOSSign   `yaml:"macos"`
	Linux   *LinuxSign   `yaml:"linux"`
}

type WindowsSign struct {
	Enabled      bool   `yaml:"enabled"`
	Tool         string `yaml:"tool"`          // signtool | osslsigncode (default: auto-detect)
	CertEnv      string `yaml:"cert_env"`      // env -> .pfx path OR base64-encoded .pfx
	PasswordEnv  string `yaml:"password_env"`  // env -> .pfx password
	Thumbprint   string `yaml:"thumbprint"`    // use a cert already in the Windows store (signtool)
	TimestampURL string `yaml:"timestamp_url"` // RFC3161 server (default: digicert)
}

type MacOSSign struct {
	Enabled            bool   `yaml:"enabled"`
	Identity           string `yaml:"identity"`     // "Developer ID Application: Name (TEAMID)"
	IdentityEnv        string `yaml:"identity_env"` // or via env
	Entitlements       string `yaml:"entitlements"` // path to entitlements.plist
	Notarize           bool   `yaml:"notarize"`
	KeychainProfileEnv string `yaml:"keychain_profile_env"` // notarytool --keychain-profile (preferred)
	AppleIDEnv         string `yaml:"apple_id_env"`
	TeamIDEnv          string `yaml:"team_id_env"`
	PasswordEnv        string `yaml:"password_env"` // app-specific password
}

type LinuxSign struct {
	Enabled   bool   `yaml:"enabled"`
	GPGKey    string `yaml:"gpg_key"`     // key id/fingerprint (not secret)
	GPGKeyEnv string `yaml:"gpg_key_env"` // or via env
}

func (bc *BuildContext) gracefulSkip(platform string, haveCreds bool) bool {
	if bc.ForceSign || haveCreds {
		return false
	}
	unsignedWarn.Do(func() {
		fmt.Printf("==> warning: %s code signing is enabled but no credentials found — producing an UNSIGNED build.\n"+
			"    Set the signing secrets to sign, or pass --skip-sign to silence this (--sign would make it a hard error).\n", platform)
	})
	return true
}

var unsignedWarn sync.Once

func (bc *BuildContext) winSign() *WindowsSign {
	if bc.SkipSign || bc.pkg.Sign == nil {
		return nil
	}
	w := bc.pkg.Sign.Windows
	if w == nil || !(w.Enabled || bc.ForceSign) {
		return nil
	}
	if bc.gracefulSkip("windows", w.Thumbprint != "" || envVal(w.CertEnv) != "") {
		return nil
	}
	return w
}

func (bc *BuildContext) macSign() *MacOSSign {
	if bc.SkipSign || bc.pkg.Sign == nil {
		return nil
	}
	m := bc.pkg.Sign.MacOS
	if m == nil || !(m.Enabled || bc.ForceSign) {
		return nil
	}
	if bc.gracefulSkip("macos", firstNonEmpty(m.Identity, envVal(m.IdentityEnv)) != "") {
		return nil
	}
	return m
}

func (bc *BuildContext) linuxSign() *LinuxSign {
	if bc.SkipSign || bc.pkg.Sign == nil {
		return nil
	}
	l := bc.pkg.Sign.Linux
	if l == nil || !(l.Enabled || bc.ForceSign) {
		return nil
	}
	if bc.gracefulSkip("linux", hasTool("gpg")) {
		return nil
	}
	return l
}

// signWindows code-signs a file (exe or msi) with signtool or osslsigncode.
// No-op when signing is disabled/skipped.
func signWindows(ctx context.Context, bc *BuildContext, file string) error {
	cfg := bc.winSign()
	if cfg == nil {
		return nil
	}

	tool := cfg.Tool
	if tool == "" {
		switch {
		case hasTool("signtool"):
			tool = "signtool"
		case hasTool("osslsigncode"):
			tool = "osslsigncode"
		default:
			return fmt.Errorf("windows signing enabled but neither signtool nor osslsigncode is in PATH")
		}
	}
	ts := firstNonEmpty(cfg.TimestampURL, "http://timestamp.digicert.com")

	switch tool {
	case "signtool":
		args := []string{"sign", "/fd", "SHA256", "/tr", ts, "/td", "SHA256"}
		if cfg.Thumbprint != "" {
			args = append(args, "/sha1", cfg.Thumbprint)
		} else {
			pfx, cleanup, err := resolvePfx(cfg.CertEnv)
			if err != nil {
				return err
			}
			if cleanup != nil {
				defer cleanup()
			}
			args = append(args, "/f", pfx)
			if pw := envVal(cfg.PasswordEnv); pw != "" {
				args = append(args, "/p", pw)
			}
		}
		args = append(args, file)
		return runTool(ctx, bc, "signtool", args, "==> signing (signtool): "+filepath.Base(file))

	case "osslsigncode":
		pfx, cleanup, err := resolvePfx(cfg.CertEnv)
		if err != nil {
			return err
		}
		if cleanup != nil {
			defer cleanup()
		}
		tmp := file + ".signed"
		args := []string{"sign", "-pkcs12", pfx}
		if pw := envVal(cfg.PasswordEnv); pw != "" {
			args = append(args, "-pass", pw)
		}
		args = append(args, "-h", "sha256", "-ts", ts, "-in", file, "-out", tmp)
		if err := runTool(ctx, bc, "osslsigncode", args, "==> signing (osslsigncode): "+filepath.Base(file)); err != nil {
			return err
		}
		return os.Rename(tmp, file)

	default:
		return fmt.Errorf("unknown windows sign tool %q (use signtool or osslsigncode)", tool)
	}
}

// codesignMac signs a macOS .app bundle or file with the configured Developer
// ID. No-op when signing is disabled/skipped.
func codesignMac(ctx context.Context, bc *BuildContext, path string, deep bool) error {
	cfg := bc.macSign()
	if cfg == nil {
		return nil
	}
	identity := firstNonEmpty(cfg.Identity, envVal(cfg.IdentityEnv))
	if identity == "" {
		return fmt.Errorf("macos signing enabled but no identity (set sign.macos.identity or identity_env)")
	}
	args := []string{"--force", "--options", "runtime", "--timestamp"}
	if deep {
		args = append(args, "--deep")
	}
	if cfg.Entitlements != "" {
		ent := cfg.Entitlements
		if !filepath.IsAbs(ent) {
			ent = filepath.Join(bc.Root, ent)
		}
		args = append(args, "--entitlements", ent)
	}
	args = append(args, "--sign", identity, path)
	return runTool(ctx, bc, "codesign", args, "==> codesign: "+filepath.Base(path))
}

// notarizeMac submits a file to Apple notarytool, waits, and staples the ticket.
// No-op when signing/notarization is disabled.
func notarizeMac(ctx context.Context, bc *BuildContext, file string) error {
	cfg := bc.macSign()
	if cfg == nil || !cfg.Notarize {
		return nil
	}
	args := []string{"notarytool", "submit", file, "--wait"}
	if prof := envVal(cfg.KeychainProfileEnv); prof != "" {
		args = append(args, "--keychain-profile", prof)
	} else {
		id, team, pw := envVal(cfg.AppleIDEnv), envVal(cfg.TeamIDEnv), envVal(cfg.PasswordEnv)
		if id == "" || team == "" || pw == "" {
			return fmt.Errorf("macos notarize: set keychain_profile_env, or apple_id_env + team_id_env + password_env")
		}
		args = append(args, "--apple-id", id, "--team-id", team, "--password", pw)
	}
	if err := runTool(ctx, bc, "xcrun", args, "==> notarytool submit: "+filepath.Base(file)); err != nil {
		return err
	}
	return runTool(ctx, bc, "xcrun", []string{"stapler", "staple", file}, "==> stapler staple: "+filepath.Base(file))
}

// signLinux writes a GPG detached signature (<file>.sig) next to the artifact.
func signLinux(ctx context.Context, bc *BuildContext, file string) error {
	cfg := bc.linuxSign()
	if cfg == nil {
		return nil
	}
	if !hasTool("gpg") {
		return fmt.Errorf("linux signing enabled but gpg not found in PATH")
	}
	args := []string{"--batch", "--yes", "--detach-sign", "--armor"}
	if key := firstNonEmpty(cfg.GPGKey, envVal(cfg.GPGKeyEnv)); key != "" {
		args = append(args, "-u", key)
	}
	args = append(args, "-o", file+".sig", file)
	return runTool(ctx, bc, "gpg", args, "==> gpg detach-sign: "+filepath.Base(file))
}

// resolvePfx returns a path to the signing .pfx. The env value may be a file
// path or a base64-encoded .pfx (decoded to a temp file; cleanup removes it).
func resolvePfx(certEnv string) (string, func(), error) {
	v := envVal(certEnv)
	if v == "" {
		return "", nil, fmt.Errorf("windows signing: cert env %q is empty (set it to a .pfx path or base64-encoded .pfx)", certEnv)
	}
	if _, err := os.Stat(v); err == nil {
		return v, nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
	if err != nil {
		return "", nil, fmt.Errorf("windows signing: cert env %q is neither a file path nor base64", certEnv)
	}
	f, err := os.CreateTemp("", "scorix-*.pfx")
	if err != nil {
		return "", nil, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	f.Close()
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

func envVal(name string) string {
	if name == "" {
		return ""
	}
	return os.Getenv(name)
}

func hasTool(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// runTool executes an external tool. It intentionally does NOT print args, which
// may carry passwords/identities — only the provided message.
func runTool(ctx context.Context, bc *BuildContext, bin string, args []string, msg string) error {
	path, err := exec.LookPath(bin)
	if err != nil {
		return fmt.Errorf("%s not found in PATH", bin)
	}
	if msg != "" {
		fmt.Println(msg)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = bc.Root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
