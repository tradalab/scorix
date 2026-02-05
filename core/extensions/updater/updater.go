package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/tradalab/scorix/core/extension"
	"github.com/tradalab/scorix/internal/logger"
	"golang.org/x/mod/semver"
)

type UpdaterExt struct {
	cfg *Config
}

func (e *UpdaterExt) Name() string {
	return "updater"
}

func (e *UpdaterExt) Init(ctx context.Context) (err error) {
	logger.Info("[updater] init")

	if v, ok := extension.GetConfigPath(ctx, "extensions.updater"); ok {
		e.cfg, err = extension.Decode[*Config](v)
		if err != nil {
			panic(err)
		}
	}

	extension.Expose(e, "CheckForUpdate")
	extension.Expose(e, "FullUpdate")

	if os.Getenv("SCORIX_UPDATER") == "1" {
		logger.Info("[updater] replace AppImage")
		runAppImageReplace()
		return
	}

	return nil
}

func (e *UpdaterExt) Stop(ctx context.Context) error {
	logger.Info("[updater] stop")
	return nil
}

func defaultClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

func (e *UpdaterExt) CheckForUpdate() (*Result, error) {
	body, err := httpGet(context.Background(), defaultClient(), e.cfg.AppcastURL)
	if err != nil {
		return nil, err
	}

	// try parse Static
	var stat StaticAppcast
	if json.Unmarshal(body, &stat) == nil && stat.Version != "" && len(stat.Platforms) > 0 {
		plat, ok := stat.Platforms[e.cfg.PlatformKey]
		if !ok {
			return nil, fmt.Errorf("platform %s not found in appcast", e.cfg.PlatformKey)
		}
		if !isNewer(stat.Version, e.cfg.CurrentVersion) {
			return &Result{HasUpdate: false}, ErrNoUpdate
		}
		return &Result{
			HasUpdate:   true,
			NewVersion:  stat.Version,
			Notes:       stat.Notes,
			ArtifactURL: plat.URL,
			SigBase64:   plat.SignatureBase64,
			Elevate:     e.cfg.ForceElevate || plat.WithElevatedTask,
		}, nil
	}

	// try parse Dynamic
	var dyn DynamicAppcast
	if json.Unmarshal(body, &dyn) == nil && dyn.URL != "" && dyn.Version != "" {
		if !isNewer(dyn.Version, e.cfg.CurrentVersion) {
			return &Result{HasUpdate: false}, ErrNoUpdate
		}
		return &Result{
			HasUpdate:   true,
			NewVersion:  dyn.Version,
			Notes:       dyn.Notes,
			ArtifactURL: dyn.URL,
			SigBase64:   dyn.SignatureBase64,
			Elevate:     e.cfg.ForceElevate,
		}, nil
	}

	return nil, ErrUnknownAppcastType
}

func isNewer(remote, local string) bool {
	// check prefix 'v' cho semver lib
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

func httpGet(ctx context.Context, c *http.Client, url string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "Scorix-Plugin-Updater/1.0")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Dynamic appcast: return 204
	if resp.StatusCode == http.StatusNoContent {
		return nil, ErrNoUpdate
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bad status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (e *UpdaterExt) Download(ctx context.Context, c *http.Client, url string) (string, error) {
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

	// TODO handler value -1
	total := resp.ContentLength

	tmpFile, err := os.CreateTemp("", filepath.Base(req.URL.Path)+"-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer tmpFile.Close()

	buf := make([]byte, 32*1024) // 32KB buffer
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
				logger.Info(fmt.Sprintf("\rDownloading... %.2f%%", percent))
			} else {
				logger.Info(fmt.Sprintf("\rDownloading... %d bytes", downloaded))
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("read body: %w", err)
		}
	}
	logger.Info("\nDownload completed!")

	return tmpFile.Name(), nil
}

func (e *UpdaterExt) VerifyEd25519(publicKeyB64, signatureB64 string, payload []byte) error {
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

	if subtle.ConstantTimeByteEq(byte(boolToByte(ok)), 1) != 1 {
		return ErrSignatureInvalid
	}
	return nil
}

func boolToByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// FullUpdate flow: check -> download -> verify -> run
func (e *UpdaterExt) FullUpdate() (*Result, error) {
	ctx := context.Background()

	res, err := e.CheckForUpdate()
	if err != nil {
		return res, err
	}
	if !res.HasUpdate {
		return res, ErrNoUpdate
	}

	localPath, err := e.Download(ctx, defaultClient(), res.ArtifactURL)
	if err != nil {
		return res, err
	}
	res.LocalPath = localPath

	switch runtime.GOOS {
	case "windows":
		err = RunMSI(ctx, localPath, res.Elevate)
	case "linux":
		err = RunAppImage(ctx, localPath)
	default:
		err = fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	if err != nil {
		return res, err
	}

	return res, nil
}

// RunMSI run file .exe, can use elevation
func RunMSI(ctx context.Context, path string, elevate bool) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("installer run only implemented for Windows")
	}

	args := []string{"/i", path, "/norestart"}

	if !elevate {
		cmd := exec.CommandContext(ctx, "msiexec.exe", args...)
		cmd.Dir = filepath.Dir(path)
		return cmd.Run()
	}

	// Elevate with PowerShell
	ps := fmt.Sprintf(`Start-Process -FilePath "msiexec.exe" -ArgumentList '%s' -Verb RunAs -Wait`, `/i "`+path+`" /norestart`)
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ps)
	cmd.Dir = filepath.Dir(path)

	return cmd.Run()
}

// RunAppImage run file .AppImage
func RunAppImage(ctx context.Context, newImage string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}

	if err := os.Chmod(newImage, 0755); err != nil {
		return fmt.Errorf("chmod appimage: %w", err)
	}

	return spawnAppImageUpdater(ctx, self, newImage)
}

func spawnAppImageUpdater(ctx context.Context, current, next string) error {
	cmd := exec.CommandContext(ctx, current, "--appimage-update", current, next)
	cmd.Env = append(os.Environ(), "SCORIX_UPDATER=1")
	return cmd.Start()
}

func runAppImageReplace() {
	if len(os.Args) < 3 {
		os.Exit(1)
	}

	current := os.Args[1]
	next := os.Args[2]

	backup := current + ".bak"

	_ = os.Remove(backup)
	_ = os.Rename(current, backup)

	if err := os.Rename(next, current); err != nil {
		_ = os.Rename(backup, current)
		os.Exit(1)
	}

	_ = os.Chmod(current, 0755)

	// restart app
	cmd := exec.Command(current)
	cmd.Start()
}

func init() {
	extension.Register(&UpdaterExt{})
}
