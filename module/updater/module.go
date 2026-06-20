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

// SEALED (no `env` tags): the update source, signing key and platform key decide
// what code we download and trust — env/runtime-file override would be an RCE
// vector, so changing them needs a rebuild with a new embedded manifest.
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
	dataDir  string // holds the anti-rollback floor file; empty → fallback under os.UserConfigDir()
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
	// Config is sealed: this logs and drops any env/runtime-file override attempt.
	if err := ctx.ApplyOverrides(&m.cfg); err != nil {
		return fmt.Errorf("apply overrides: %w", err)
	}

	m.dataDir = ctx.DataDir

	// SECURITY: AppVersion/dataDir derive from env-overridable app.version/app.name,
	// so env control can lower the floor base or move the floor file. Foothold-gated
	// and defended in depth (on-disk floor wins via max() in rollbackFloor; every
	// update must be strictly newer AND pass Ed25519). TODO: anchor CurrentVersion to
	// a build-stamped constant and the floor file to the SEALED app.identifier.
	if m.cfg.CurrentVersion == "" {
		m.cfg.CurrentVersion = ctx.AppVersion
	}

	if m.cfg.PlatformKey == "" {
		m.cfg.PlatformKey = fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH) // e.g. windows-amd64
	}

	if m.cfg.Provider == "github" {
		m.provider = NewGitHubProvider(m.cfg.GitHubRepo)
		logger.Info(fmt.Sprintf("[updater] using GitHub provider: repo=%s, platform=%s", m.cfg.GitHubRepo, m.cfg.PlatformKey))
	} else {
		// Public key lets the provider authenticate the manifest before trusting its fields.
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
		// Every redirect hop must stay secure: cross-host https is fine (CDN), but a
		// downgrade to plain http on a remote host (MITM) is refused.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return checkSecureURL(req.URL.String())
		},
	}
}

// Rejects non-https update URLs (loopback http allowed for local testing) to block MITM/downgrade.
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

// Anti-rollback floor: highest version ever accepted; reject anything not strictly
// newer than max(CurrentVersion, floor), defeating signed-but-old replays.

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

// Best-effort: a write failure is logged, never fatal — must not abort an update.
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

// max(CurrentVersion, persistedFloor) — the version an update must EXCEED to be accepted.
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
	// Private 0700 dir so no other local process can swap the artifact between verify
	// and install (TOCTOU); extension constrained to a known installer type so a
	// hostile URL can't pick a suffix that flows into the installer.
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

// Restricts the file extension to a known installer type (per-OS default) so a hostile
// artifact URL can't pick a suffix that flows into msiexec / the OS installer.
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

// Package-level so AppcastProvider can verify the manifest without a module receiver.
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
	// ed25519.Verify is already constant-time; no subtle.ConstantTime needed.
	if !ed25519.Verify(ed25519.PublicKey(pub), payload, sig) {
		return ErrSignatureInvalid
	}
	return nil
}

// On darwin/linux this self-replaces and os.Exit()s after scheduling a relaunch — does NOT return on success.
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
	// Path is interpolated into a PowerShell string; refuse chars that break quoting and inject commands.
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

// Errors if not running from inside a .app bundle (e.g. a dev build).
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

// Detached shell that waits for this process to exit (releasing the single-instance
// lock), then runs the command. Best-effort.
func relaunchAfterExit(args ...string) {
	// Command passed as positional params ("$@"), not interpolated, so no shell
	// injection; only the integer PID is interpolated.
	script := fmt.Sprintf(`while kill -0 %d 2>/dev/null; do sleep 0.2; done; exec "$@"`, os.Getpid())
	cmd := exec.Command("sh", append([]string{"-c", script, "sh"}, args...)...)
	_ = cmd.Start() // detached; orphaned to init after we exit
}

// JS: scorix.invoke("mod:updater:CheckForUpdate", null)
func (m *UpdaterModule) CheckForUpdate(ctx context.Context) (*Result, error) {
	if m.provider == nil {
		return nil, fmt.Errorf("update provider not initialized")
	}

	// Check against the floor, not CurrentVersion: a replayed old-but-signed manifest
	// must not downgrade us even if the binary reports a stale CurrentVersion.
	floor := m.rollbackFloor()
	res, err := m.provider.CheckForUpdate(ctx, floor, m.cfg.PlatformKey)
	if err != nil {
		return res, err
	}

	// Defence in depth: FAIL CLOSED — re-check the floor even if the provider said HasUpdate.
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

	// Remove the private download dir on every failure path (else failed attempts and
	// unverified artifacts pile up in TEMP); disarmed once the installer owns the file.
	tmpDir := filepath.Dir(localPath)
	installing := false
	defer func() {
		if !installing {
			_ = os.RemoveAll(tmpDir)
		}
	}()

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
	installing = true // installer owns the file from here; don't delete it

	// Raise the floor BEFORE the installer: RunInstaller may never return, so
	// persisting afterwards is unreliable. Signatures already passed, so committing now is safe.
	m.writeFloor(res.NewVersion)

	logger.Info(fmt.Sprintf("[updater] Running installer at: %s", localPath))
	if err := RunInstaller(ctx, localPath, res.Elevate); err != nil {
		return res, err
	}
	return res, nil
}
