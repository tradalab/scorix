package runner

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// UpdateConfig is the optional `package.update` block. It drives `scorix appcast`,
// which signs release artifacts (Ed25519), writes SHA256SUMS and an appcast.json
// manifest consumed by the updater module.
type UpdateConfig struct {
	Appcast    bool   `yaml:"appcast"`      // emit appcast.json
	BaseURL    string `yaml:"base_url"`     // artifacts are served under this URL
	SignKeyEnv string `yaml:"sign_key_env"` // env -> base64 Ed25519 private key
	Checksums  bool   `yaml:"checksums"`    // emit SHA256SUMS
	Elevate    bool   `yaml:"elevate"`      // with_elevated_task for windows entries
	Notes      string `yaml:"notes"`        // optional release notes
}

// appcast manifest shapes — mirror module/updater StaticAppcast/PlatformArtifact.
type staticAppcast struct {
	Version   string                      `json:"version"`
	PubDate   string                      `json:"pub_date,omitempty"`
	Notes     string                      `json:"notes,omitempty"`
	Platforms map[string]platformArtifact `json:"platforms"`
}

type platformArtifact struct {
	URL              string `json:"url"`
	SignatureBase64  string `json:"signature,omitempty"`
	WithElevatedTask bool   `json:"with_elevated_task,omitempty"`
}

// AppcastOptions controls `scorix appcast`.
type AppcastOptions struct {
	Dir          string
	ArtifactsDir string
	BaseURL      string // overrides package.update.base_url
}

// Appcast signs the installer artifacts, writes SHA256SUMS and appcast.json.
func Appcast(ctx context.Context, opt AppcastOptions) error {
	root, err := filepath.Abs(orDefault(opt.Dir, "."))
	if err != nil {
		return err
	}
	cfg, err := loadProjectConfig(filepath.Join(root, "scorix.yaml"))
	if err != nil {
		return fmt.Errorf("load scorix.yaml: %w", err)
	}
	meta, err := loadAppMetadata(filepath.Join(root, "etc", "app.yaml"))
	if err != nil {
		return err
	}
	upd := &UpdateConfig{}
	if cfg.Package != nil && cfg.Package.Update != nil {
		upd = cfg.Package.Update
	}

	artDir := opt.ArtifactsDir
	if artDir == "" {
		artDir = filepath.Join(root, "artifacts")
	}
	baseURL := firstNonEmpty(opt.BaseURL, upd.BaseURL)
	version := firstNonEmpty(meta.App.Version, "0.0.0")

	files, err := listInstallers(artDir)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no installer artifacts (.msi/.dmg/.AppImage) found in %s — run `scorix package` first", artDir)
	}

	priv, err := loadUpdatePrivateKey(upd.SignKeyEnv)
	if err != nil {
		return err
	}
	if priv == nil {
		fmt.Println("note: no signing key (package.update.sign_key_env unset/empty) — appcast entries will be UNSIGNED")
	}

	var sums []string
	platforms := map[string]platformArtifact{}

	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		base := filepath.Base(f)

		sum := sha256.Sum256(data)
		sums = append(sums, fmt.Sprintf("%s  %s", hex.EncodeToString(sum[:]), base))

		var sigB64 string
		if priv != nil {
			sigB64 = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data))
			if err := os.WriteFile(f+".sig", []byte(sigB64), 0o644); err != nil {
				return err
			}
			fmt.Printf("==> signed %s -> %s.sig\n", base, base)
		}

		keys := platformKeysForArtifact(base)
		if len(keys) == 0 {
			fmt.Printf("warning: could not derive platform key for %s — skipping appcast entry\n", base)
			continue
		}
		for _, k := range keys {
			if _, dup := platforms[k]; dup {
				fmt.Printf("warning: two artifacts map to platform %q — overwriting with %s (remove stale artifacts so the appcast points at one build)\n", k, base)
			}
			platforms[k] = platformArtifact{
				URL:              joinURL(baseURL, base),
				SignatureBase64:  sigB64,
				WithElevatedTask: upd.Elevate && strings.HasPrefix(k, "windows-"),
			}
		}
	}

	if upd.Checksums {
		sort.Strings(sums)
		out := filepath.Join(artDir, "SHA256SUMS")
		if err := os.WriteFile(out, []byte(strings.Join(sums, "\n")+"\n"), 0o644); err != nil {
			return err
		}
		fmt.Printf("==> wrote %s\n", out)
	}

	if upd.Appcast {
		ac := staticAppcast{
			Version:   version,
			PubDate:   time.Now().UTC().Format(time.RFC3339),
			Notes:     upd.Notes,
			Platforms: platforms,
		}
		data, err := json.MarshalIndent(ac, "", "  ")
		if err != nil {
			return err
		}
		out := filepath.Join(artDir, "appcast.json")
		if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Printf("==> wrote %s (%d platform entries)\n", out, len(platforms))
	}
	return nil
}

// GenerateKeypair prints a fresh Ed25519 keypair (base64). The public key goes
// into modules.updater.public_key_base_64; keep the private key secret (CI env).
func GenerateKeypair() error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	fmt.Println("PUBLIC_KEY_B64 =", base64.StdEncoding.EncodeToString(pub))
	fmt.Println("PRIVATE_KEY_B64 =", base64.StdEncoding.EncodeToString(priv))
	fmt.Println("\nSet the public key in etc/app.yaml (modules.updater.public_key_base_64).")
	fmt.Println("Keep the private key secret; expose it to `scorix appcast` via the env named in package.update.sign_key_env.")
	return nil
}

func loadUpdatePrivateKey(env string) (ed25519.PrivateKey, error) {
	v := envVal(env)
	if v == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
	if err != nil {
		return nil, fmt.Errorf("update sign key (env %s) is not valid base64: %w", env, err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("update sign key (env %s) has wrong size %d (want %d) — generate with `scorix keygen`", env, len(raw), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(raw), nil
}

func listInstallers(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(d.Name())) {
		case ".msi", ".dmg", ".appimage":
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan artifacts dir %s: %w", dir, err)
	}
	sort.Strings(out)
	return out, nil
}

// platformKeysForArtifact maps an artifact filename to the updater's platform
// key(s) ({GOOS}-{GOARCH}). A universal darwin build serves both arches.
func platformKeysForArtifact(name string) []string {
	lower := strings.ToLower(name)
	var goos string
	switch {
	case strings.HasSuffix(lower, ".msi"):
		goos = "windows"
	case strings.HasSuffix(lower, ".dmg"):
		goos = "darwin"
	case strings.HasSuffix(lower, ".appimage"):
		goos = "linux"
	default:
		return nil
	}

	var archs []string
	switch {
	case strings.Contains(lower, "universal"):
		if goos == "darwin" {
			archs = []string{"amd64", "arm64"}
		} else {
			archs = []string{"amd64"}
		}
	case strings.Contains(lower, "arm64"), strings.Contains(lower, "aarch64"):
		archs = []string{"arm64"}
	case strings.Contains(lower, "amd64"), strings.Contains(lower, "x86_64"), strings.Contains(lower, "x64"):
		archs = []string{"amd64"}
	default:
		archs = []string{"amd64"}
	}

	out := make([]string, 0, len(archs))
	for _, a := range archs {
		out = append(out, goos+"-"+a)
	}
	return out
}

func joinURL(base, name string) string {
	if base == "" {
		return name
	}
	return strings.TrimRight(base, "/") + "/" + name
}
