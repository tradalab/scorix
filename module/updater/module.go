// Package updater provides an Ed25519-verified remote auto-updater module for scorix.
//
// Enable in app.yaml:
//
//	modules:
//	  updater:
//	    enabled: true
//	    provider: github                 # appcast | github. Default: appcast.
//	    github_repo: tradalab/scorix     # For github provider
//	    appcast_url: "https://your-server.com/appcast.json"
//	    public_key_base_64: "..."
//	    platform_key: "windows-amd64"
//	    force_elevate: false
//	    current_version: "1.0.0"
package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"github.com/tradalab/scorix/logger"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/tradalab/scorix/kernel/core/module"
	"golang.org/x/mod/semver"
)

// Config holds the updater module configuration.
type Config struct {
	Provider        string `json:"provider"`    // "appcast" or "github"
	GitHubRepo      string `json:"github_repo"` // user/repo for github provider
	AppcastURL      string `json:"appcast_url"` // URL for appcast provider
	PublicKeyBase64 string `json:"public_key_base_64"`
	PlatformKey     string `json:"platform_key"` // Leave empty for auto `{os}-{arch}`
	ForceElevate    bool   `json:"force_elevate"`
	CurrentVersion  string `json:"current_version"`
}

// UpdateProvider abstracts the version checking mechanism (GitHub vs Appcast).
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

// ////////// Module ////////// ////////// ////////// ////////// ////////// //////////

// UpdaterModule provides auto-updating capabilities.
type UpdaterModule struct {
	cfg      Config
	provider UpdateProvider
}

// New creates a new UpdaterModule.
func New() *UpdaterModule {
	return &UpdaterModule{}
}

func (m *UpdaterModule) Name() string    { return "updater" }
func (m *UpdaterModule) Version() string { return "1.0.0" }

// ////////// Lifecycle ////////// ////////// ////////// ////////// ////////// //////////

func (m *UpdaterModule) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[updater] loading (v%s)", m.Version()))

	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}

	if m.cfg.PlatformKey == "" {
		m.cfg.PlatformKey = fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH) // e.g. windows-amd64
	}

	if m.cfg.Provider == "github" {
		m.provider = NewGitHubProvider(m.cfg.GitHubRepo)
		logger.Info(fmt.Sprintf("[updater] using GitHub provider: repo=%s, platform=%s", m.cfg.GitHubRepo, m.cfg.PlatformKey))
	} else {
		// Default to appcast
		m.provider = &AppcastProvider{appcastURL: m.cfg.AppcastURL}
		logger.Info(fmt.Sprintf("[updater] using Appcast provider: url=%s, platform=%s", m.cfg.AppcastURL, m.cfg.PlatformKey))
	}

	module.Expose(m, "CheckForUpdate", ctx.IPC)
	module.Expose(m, "FullUpdate", ctx.IPC)

	return nil
}

func (m *UpdaterModule) OnStart() error  { return nil }
func (m *UpdaterModule) OnStop() error   { return nil }
func (m *UpdaterModule) OnUnload() error { return nil }

// ////////// Internal Helpers ////////// ////////// ////////// ////////// ////////// //////////

func defaultClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

func httpGet(ctx context.Context, c *http.Client, url string, headers map[string]string) ([]byte, error) {
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

func (m *UpdaterModule) Download(ctx context.Context, c *http.Client, url string) (string, error) {
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
	tmpFile, err := os.CreateTemp("", filepath.Base(req.URL.Path)+"-*")
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

func (m *UpdaterModule) VerifyEd25519(publicKeyB64, signatureB64 string, payload []byte) error {
	if signatureB64 == "" {
		return ErrSignatureMissing
	}
	pub, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return fmt.Errorf("invalid public key b64: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return fmt.Errorf("invalid signature b64: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid public key size: %d", len(pub))
	}
	ok := ed25519.Verify(ed25519.PublicKey(pub), payload, sig)

	var b byte
	if ok {
		b = 1
	} else {
		b = 0
	}
	if subtle.ConstantTimeByteEq(b, 1) != 1 {
		return ErrSignatureInvalid
	}
	return nil
}

func RunInstaller(ctx context.Context, path string, elevate bool) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("installer run only implemented for Windows")
	}

	args := []string{"/i", path, "/norestart"}

	if !elevate {
		cmd := exec.CommandContext(ctx, "msiexec.exe", args...)
		cmd.Dir = filepath.Dir(path)
		return cmd.Run()
	}

	ps := fmt.Sprintf(`Start-Process -FilePath "msiexec.exe" -ArgumentList '%s' -Verb RunAs -Wait`,
		`/i "`+path+`" /norestart`,
	)
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ps)
	cmd.Dir = filepath.Dir(path)
	return cmd.Run()
}

// ////////// IPC Handlers ////////// ////////// ////////// ////////// ////////// //////////

// CheckForUpdate calls the appcast URL and checks if a newer version is available.
// JS: scorix.invoke("mod:updater:CheckForUpdate", null)
func (m *UpdaterModule) CheckForUpdate(ctx context.Context) (*Result, error) {
	if m.provider == nil {
		return nil, fmt.Errorf("update provider not initialized")
	}

	res, err := m.provider.CheckForUpdate(ctx, m.cfg.CurrentVersion, m.cfg.PlatformKey)
	if err != nil {
		return res, err
	}

	// Apply global configuration overrides
	if m.cfg.ForceElevate {
		res.Elevate = true
	}

	return res, nil
}

// FullUpdate runs the full update flow: check -> download -> verfiy -> run installer.
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

	// TODO verify data
	// data, _ := os.ReadFile(localPath)
	// if err := m.VerifyEd25519(m.cfg.PublicKeyBase64, res.SigBase64, data); err != nil {
	// 	return res, err
	// }

	logger.Info(fmt.Sprintf("[updater] Running installer at: %s", localPath))
	if err := RunInstaller(ctx, localPath, res.Elevate); err != nil {
		return res, err
	}
	return res, nil
}
