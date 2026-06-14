// Package updater is an Ed25519-verified remote auto-updater module.
package updater

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/tradalab/scorix/logger"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/tradalab/scorix/module"
	"golang.org/x/mod/semver"
)

type Config struct {
	Provider        string `json:"provider"`    // "appcast" or "github"
	GitHubRepo      string `json:"github_repo"` // user/repo for github provider
	AppcastURL      string `json:"appcast_url"` // URL for appcast provider
	PublicKeyBase64 string `json:"public_key_base_64"`
	PlatformKey     string `json:"platform_key"` // Leave empty for auto `{os}-{arch}`
	ForceElevate    bool   `json:"force_elevate"`
	CurrentVersion  string `json:"current_version"`
}

type UpdateProvider interface {
	CheckForUpdate(ctx context.Context, currentVersion, platformKey string) (*Result, error)
}

type Result struct {
	HasUpdate   bool   `json:"has_update"`
	NewVersion  string `json:"new_version"`
	Notes       string `json:"notes"`
	ArtifactURL string `json:"artifact_url"`
	SigBase64   string `json:"sig_base64"`
	Elevate     bool   `json:"elevate"`
	LocalPath   string `json:"local_path"`
}

var (
	ErrNoUpdate           = errors.New("no update available")
	ErrSignatureMissing   = errors.New("signature missing in appcast")
	ErrSignatureInvalid   = errors.New("signature invalid")
	ErrUnknownAppcastType = errors.New("unknown appcast shape")
)

type UpdaterModule struct {
	cfg      Config
	provider UpdateProvider
	// dataDir holds the anti-rollback floor file. Empty -> best-effort
	// fallback under os.UserConfigDir().
	dataDir string
}

func New() *UpdaterModule {
	return &UpdaterModule{}
}

func (m *UpdaterModule) Name() string    { return "updater" }
func (m *UpdaterModule) Version() string { return "1.0.0" }

func (m *UpdaterModule) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[updater] loading (v%s)", m.Version()))

	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}

	m.dataDir = ctx.DataDir

	if m.cfg.PlatformKey == "" {
		m.cfg.PlatformKey = fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH) // e.g. windows-amd64
	}

	if m.cfg.Provider == "github" {
		m.provider = NewGitHubProvider(m.cfg.GitHubRepo)
		logger.Info(fmt.Sprintf("[updater] using GitHub provider: repo=%s, platform=%s", m.cfg.GitHubRepo, m.cfg.PlatformKey))
	} else {
		// Pass the public key so the provider authenticates the manifest before
		// trusting its version/url fields.
		m.provider = NewAppcastProvider(m.cfg.AppcastURL, m.cfg.PublicKeyBase64)
		logger.Info(fmt.Sprintf("[updater] using Appcast provider: url=%s, platform=%s", m.cfg.AppcastURL, m.cfg.PlatformKey))
	}

	module.Expose(m, "CheckForUpdate", ctx.IPC)
	module.Expose(m, "FullUpdate", ctx.IPC)

	return nil
}

func (m *UpdaterModule) OnStart() error  { return nil }
func (m *UpdaterModule) OnStop() error   { return nil }
func (m *UpdaterModule) OnUnload() error { return nil }

func defaultClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		// Every redirect hop must stay secure (https, or http to loopback for
		// local testing). Cross-host https IS allowed — release downloads
		// legitimately redirect to a CDN — but a downgrade to plain http on a
		// remote host (MITM) is refused.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return checkSecureURL(req.URL.String())
		},
	}
}

// checkSecureURL rejects update URLs that aren't https — except plain http to a
// loopback host, which is permitted for local testing. This blocks MITM /
// downgrade attacks on appcast, artifact and signature fetches.
func checkSecureURL(rawurl string) error {
	u, err := url.Parse(rawurl)
	if err != nil {
		return fmt.Errorf("invalid update URL: %w", err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			return nil
		}
		return fmt.Errorf("insecure update URL (plain http to non-loopback host %q): refusing", u.Host)
	default:
		return fmt.Errorf("unsupported update URL scheme %q (want https)", u.Scheme)
	}
}

func httpGet(ctx context.Context, c *http.Client, url string, headers map[string]string) ([]byte, error) {
	if err := checkSecureURL(url); err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "Scorix-Module-Updater/1.0")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, ErrNoUpdate
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bad status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func isNewer(remote, local string) bool {
	r := ensureV(remote)
	l := ensureV(local)
	return semver.IsValid(r) && semver.IsValid(l) && semver.Compare(r, l) > 0
}

func ensureV(v string) string {
	if len(v) > 0 && v[0] != 'v' {
		return "v" + v
	}
	return v
}

// Anti-rollback floor: highest version ever accepted; we refuse anything not
// strictly newer than max(CurrentVersion, floor), defeating signed-but-old replays.

// floorPath prefers the per-app DataDir; falls back to os.UserConfigDir()/scorix.
// Best-effort: returns "" if no location can be determined.
func (m *UpdaterModule) floorPath() string {
	dir := m.dataDir
	if dir == "" {
		if cfg, err := os.UserConfigDir(); err == nil {
			dir = filepath.Join(cfg, "scorix")
		}
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "updater_floor")
}

func (m *UpdaterModule) readFloor() string {
	p := m.floorPath()
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// writeFloor persists the version as the new floor. Best-effort: failures are
// logged, never fatal (callers must not abort an update on a write error).
func (m *UpdaterModule) writeFloor(version string) {
	p := m.floorPath()
	if p == "" {
		logger.Info("[updater] anti-rollback: no data dir available — skipping floor persist")
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		logger.Info(fmt.Sprintf("[updater] anti-rollback: mkdir floor dir failed (best-effort): %v", err))
		return
	}
	if err := os.WriteFile(p, []byte(version+"\n"), 0o600); err != nil {
		logger.Info(fmt.Sprintf("[updater] anti-rollback: write floor failed (best-effort): %v", err))
		return
	}
}

// rollbackFloor returns max(cfg.CurrentVersion, persistedFloor) as the minimum
// version an update must EXCEED to be accepted.
func (m *UpdaterModule) rollbackFloor() string {
	floor := m.cfg.CurrentVersion
	if persisted := m.readFloor(); isNewer(persisted, floor) {
		floor = persisted
	}
	return floor
}

func (m *UpdaterModule) Download(ctx context.Context, c *http.Client, url string) (string, error) {
	if err := checkSecureURL(url); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download status %d", resp.StatusCode)
	}

	total := resp.ContentLength
	// Download into a private, current-user-only directory (0700) so another
	// local process can't swap the artifact between signature verification and
	// install (TOCTOU). The extension is constrained to a known installer type —
	// msiexec rejects a file not ending in .msi, and a hostile URL must not be
	// able to choose an arbitrary suffix that flows into the installer.
	dir, err := os.MkdirTemp("", "scorix-update-")
	if err != nil {
		return "", fmt.Errorf("create update dir: %w", err)
	}
	ext := safeInstallerExt(filepath.Ext(filepath.Base(req.URL.Path)))
	tmpFile, err := os.OpenFile(filepath.Join(dir, "installer"+ext), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer tmpFile.Close()

	buf := make([]byte, 32*1024)
	var downloaded int64

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := tmpFile.Write(buf[:n]); werr != nil {
				return "", fmt.Errorf("write file: %w", werr)
			}
			downloaded += int64(n)

			if total > 0 {
				percent := float64(downloaded) / float64(total) * 100
				logger.Info(fmt.Sprintf("[updater] Downloading... %.2f%%", percent))
			} else {
				logger.Info(fmt.Sprintf("[updater] Downloading... %d bytes", downloaded))
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("read body: %w", err)
		}
	}
	logger.Info("[updater] Download completed!")

	return tmpFile.Name(), nil
}

// safeInstallerExt restricts the downloaded file's extension to a known
// installer type (defaulting per-OS) so a hostile artifact URL can't pick an
// arbitrary suffix that later flows into msiexec / the OS installer.
func safeInstallerExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".msi", ".exe", ".dmg", ".pkg", ".appimage":
		return ext
	}
	switch runtime.GOOS {
	case "windows":
		return ".msi"
	case "darwin":
		return ".dmg"
	default:
		return ".AppImage"
	}
}

func (m *UpdaterModule) VerifyEd25519(publicKeyB64, signatureB64 string, payload []byte) error {
	return verifyEd25519(publicKeyB64, signatureB64, payload)
}

// verifyEd25519 verifies an Ed25519 signature (base64) over payload using the
// base64 public key. Package-level so AppcastProvider can verify the manifest
// without a module receiver. Returns ErrSignatureMissing / ErrSignatureInvalid.
func verifyEd25519(publicKeyB64, signatureB64 string, payload []byte) error {
	if signatureB64 == "" {
		return ErrSignatureMissing
	}
	pub, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return fmt.Errorf("invalid public key b64: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signatureB64))
	if err != nil {
		return fmt.Errorf("invalid signature b64: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid public key size: %d", len(pub))
	}
	// ed25519.Verify is already constant-time and returns a plain bool; no
	// extra subtle.ConstantTime dance is needed (the if already branches on it).
	if !ed25519.Verify(ed25519.PublicKey(pub), payload, sig) {
		return ErrSignatureInvalid
	}
	return nil
}

// RunInstaller applies a downloaded, verified update for the current platform:
//   - windows: hands the .msi to msiexec (which closes/relaunches the app).
//   - darwin:  mounts the .dmg, swaps the running .app, relaunches, exits.
//   - linux:   replaces the running AppImage, relaunches, exits.
//
// The darwin/linux paths self-replace and call os.Exit after scheduling a
// relaunch, so this function does not return on those platforms on success.
func RunInstaller(ctx context.Context, path string, elevate bool) error {
	switch runtime.GOOS {
	case "windows":
		return runInstallerWindows(ctx, path, elevate)
	case "darwin":
		return runInstallerDarwin(ctx, path)
	case "linux":
		return runInstallerLinux(ctx, path)
	default:
		return fmt.Errorf("auto-install not supported on %s", runtime.GOOS)
	}
}

func runInstallerWindows(ctx context.Context, path string, elevate bool) error {
	args := []string{"/i", path, "/norestart"}
	if !elevate {
		cmd := exec.CommandContext(ctx, "msiexec.exe", args...)
		cmd.Dir = filepath.Dir(path)
		return cmd.Run()
	}
	// The path is interpolated into a PowerShell command string; refuse any
	// character that could break out of the quoting and inject commands. (After
	// the download hardening the path is updater-controlled, but guard anyway.)
	if strings.ContainsAny(path, "'\"`\r\n") {
		return fmt.Errorf("refusing to elevate-install: unsafe characters in installer path %q", path)
	}
	ps := fmt.Sprintf(`Start-Process -FilePath "msiexec.exe" -ArgumentList '%s' -Verb RunAs -Wait`,
		`/i "`+path+`" /norestart`,
	)
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ps)
	cmd.Dir = filepath.Dir(path)
	return cmd.Run()
}

// runInstallerDarwin mounts the .dmg, swaps the running .app bundle for the new
// one (rename-based, with rollback), then relaunches after this process exits.
func runInstallerDarwin(ctx context.Context, dmgPath string) error {
	appPath, err := currentMacAppBundle()
	if err != nil {
		return err
	}

	mount, err := hdiutilAttach(ctx, dmgPath)
	if err != nil {
		return err
	}
	defer hdiutilDetach(mount)

	newApp, err := findAppBundle(mount)
	if err != nil {
		return err
	}

	staged := appPath + ".new"
	_ = os.RemoveAll(staged)
	// `ditto` preserves bundle symlinks, code-signing and resource forks.
	if out, derr := exec.CommandContext(ctx, "ditto", newApp, staged).CombinedOutput(); derr != nil {
		return fmt.Errorf("copy new app: %w (%s)", derr, strings.TrimSpace(string(out)))
	}

	backup := appPath + ".old"
	_ = os.RemoveAll(backup)
	if err := os.Rename(appPath, backup); err != nil {
		_ = os.RemoveAll(staged)
		return fmt.Errorf("move current app aside: %w", err)
	}
	if err := os.Rename(staged, appPath); err != nil {
		_ = os.Rename(backup, appPath) // rollback
		return fmt.Errorf("install new app: %w", err)
	}
	_ = os.RemoveAll(backup)

	hdiutilDetach(mount) // explicit: os.Exit below skips the deferred detach
	relaunchAfterExit("open", appPath)
	logger.Info("[updater] update installed; relaunching")
	os.Exit(0)
	return nil
}

// runInstallerLinux replaces the running AppImage file with the new one, then
// relaunches after this process exits.
func runInstallerLinux(ctx context.Context, newPath string) error {
	_ = ctx
	target := currentLinuxAppImage()
	if target == "" {
		return fmt.Errorf("auto-install requires running as an AppImage (the $APPIMAGE path is not set)")
	}
	if err := os.Chmod(newPath, 0o755); err != nil {
		return err
	}

	backup := target + ".old"
	_ = os.Remove(backup)
	if err := os.Rename(target, backup); err != nil {
		return fmt.Errorf("move current AppImage aside: %w", err)
	}
	if err := copyFileTo(newPath, target); err != nil {
		_ = os.Rename(backup, target) // rollback
		return fmt.Errorf("install new AppImage: %w", err)
	}
	_ = os.Chmod(target, 0o755)
	_ = os.Remove(backup)

	relaunchAfterExit(target)
	logger.Info("[updater] update installed; relaunching")
	os.Exit(0)
	return nil
}

// currentMacAppBundle returns the absolute path to the running .app bundle, or
// an error if the binary is not running from inside one (e.g. a dev build).
func currentMacAppBundle() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	marker := ".app/Contents/MacOS/"
	if i := strings.Index(exe, marker); i >= 0 {
		return exe[:i+len(".app")], nil
	}
	return "", fmt.Errorf("not running from a .app bundle — cannot self-update (path: %s)", exe)
}

// currentLinuxAppImage returns the path of the running AppImage (set in $APPIMAGE
// by the AppImage runtime), or "" if not running as an AppImage.
func currentLinuxAppImage() string {
	if p := os.Getenv("APPIMAGE"); p != "" {
		return p
	}
	return ""
}

func hdiutilAttach(ctx context.Context, dmg string) (string, error) {
	out, err := exec.CommandContext(ctx, "hdiutil", "attach", "-nobrowse", "-readonly", dmg).Output()
	if err != nil {
		return "", fmt.Errorf("hdiutil attach: %w", err)
	}
	// The mount point is the last whitespace-separated field that starts with /Volumes/.
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.Index(line, "/Volumes/"); i >= 0 {
			return strings.TrimRight(line[i:], " \t\r"), nil
		}
	}
	return "", fmt.Errorf("could not determine dmg mount point")
}

func hdiutilDetach(mount string) {
	_ = exec.Command("hdiutil", "detach", "-quiet", mount).Run()
}

func findAppBundle(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".app") {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("no .app bundle found in %s", dir)
}

func copyFileTo(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// relaunchAfterExit spawns a detached shell that waits for this process to exit,
// then runs the given command — so the new app starts only after the old one
// releases the single-instance lock. Best-effort.
func relaunchAfterExit(args ...string) {
	// Pass the command as positional params ("$@") rather than interpolating it
	// into the script body, so no quoting/shell-injection is possible. Only the
	// PID (an integer) is interpolated. sh -c <script> sh arg1 arg2... binds
	// $0=sh and $@=args.
	script := fmt.Sprintf(`while kill -0 %d 2>/dev/null; do sleep 0.2; done; exec "$@"`, os.Getpid())
	cmd := exec.Command("sh", append([]string{"-c", script, "sh"}, args...)...)
	_ = cmd.Start() // detached; orphaned to init after we exit
}

// JS: scorix.invoke("mod:updater:CheckForUpdate", null)
func (m *UpdaterModule) CheckForUpdate(ctx context.Context) (*Result, error) {
	if m.provider == nil {
		return nil, fmt.Errorf("update provider not initialized")
	}

	// Check against the anti-rollback floor, not just CurrentVersion: a replayed
	// OLD-but-validly-signed manifest must not downgrade us even if the running
	// binary reports a stale CurrentVersion.
	floor := m.rollbackFloor()
	res, err := m.provider.CheckForUpdate(ctx, floor, m.cfg.PlatformKey)
	if err != nil {
		return res, err
	}

	// Defence in depth: even if a provider returned HasUpdate, refuse anything
	// not strictly newer than the floor (FAIL CLOSED against rollback).
	if res != nil && res.HasUpdate && !isNewer(res.NewVersion, floor) {
		return &Result{HasUpdate: false}, ErrNoUpdate
	}

	if m.cfg.ForceElevate {
		res.Elevate = true
	}

	return res, nil
}

// JS: scorix.invoke("mod:updater:FullUpdate", null)
func (m *UpdaterModule) FullUpdate(ctx context.Context) (*Result, error) {
	res, err := m.CheckForUpdate(ctx)
	if err != nil {
		return res, err
	}
	if !res.HasUpdate {
		return res, ErrNoUpdate
	}

	localPath, err := m.Download(ctx, defaultClient(), res.ArtifactURL)
	if err != nil {
		return res, err
	}
	res.LocalPath = localPath

	if m.cfg.PublicKeyBase64 == "" {
		return res, fmt.Errorf("updater: refusing to run an unverified update — set modules.updater.public_key_base_64 and sign releases with `scorix appcast`")
	}
	data, err := os.ReadFile(localPath)
	if err != nil {
		return res, fmt.Errorf("updater: read downloaded artifact: %w", err)
	}
	if err := m.VerifyEd25519(m.cfg.PublicKeyBase64, res.SigBase64, data); err != nil {
		return res, fmt.Errorf("updater: signature verification failed (refusing to install): %w", err)
	}
	logger.Info("[updater] signature verified")

	// Raise the anti-rollback floor BEFORE invoking the installer: RunInstaller
	// may exit/replace the process and never return, so persisting afterwards is
	// unreliable. We are about to install this version and the manifest +
	// artifact signatures have already passed, so committing the floor now is
	// safe. Best-effort — a persistence failure must not abort the update.
	m.writeFloor(res.NewVersion)

	logger.Info(fmt.Sprintf("[updater] Running installer at: %s", localPath))
	if err := RunInstaller(ctx, localPath, res.Elevate); err != nil {
		return res, err
	}
	return res, nil
}
