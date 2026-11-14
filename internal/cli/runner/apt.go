package runner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// LinuxPackage is the optional `package.linux` block.
type LinuxPackage struct {
	Apt *AptConfig `yaml:"apt"`
}

// AptConfig turns the .deb into a package that apt keeps up to date: the .deb
// ships a sources list and keyring for URL, and `scorix apt` writes the signed
// repository that is served there.
type AptConfig struct {
	URL       string `yaml:"url"`
	PublicKey string `yaml:"public_key"`
	Suite     string `yaml:"suite"`
	Component string `yaml:"component"`
}

const aptKeyEnv = "SCORIX_APT_PRIVATE_KEY"

func (c *AptConfig) suite() string     { return orDefault(c.Suite, "stable") }
func (c *AptConfig) component() string { return orDefault(c.Component, "main") }

func (p *PackageConfig) apt() *AptConfig {
	if p == nil || p.Linux == nil || p.Linux.Apt == nil {
		return nil
	}
	return p.Linux.Apt
}

func aptSourcesPath(pkgName string) string { return "etc/apt/sources.list.d/" + pkgName + ".list" }
func aptKeyringPath(pkgName string) string {
	return "usr/share/keyrings/" + pkgName + "-archive-keyring.asc"
}

func aptSourcesLine(c *AptConfig, pkgName string) string {
	return fmt.Sprintf("deb [signed-by=/%s] %s %s %s\n", aptKeyringPath(pkgName), strings.TrimRight(c.URL, "/"), c.suite(), c.component())
}

func readAptPublicKey(root string, c *AptConfig) ([]byte, error) {
	if c.URL == "" || c.PublicKey == "" {
		return nil, errors.New("package.linux.apt needs both url and public_key")
	}
	if !strings.HasPrefix(c.URL, "https://") && !strings.HasPrefix(c.URL, "http://") {
		return nil, fmt.Errorf("package.linux.apt.url must be an http(s) URL, got %q", c.URL)
	}
	p := c.PublicKey
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	key, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("package.linux.apt.public_key: %w", err)
	}
	if !bytes.Contains(key, []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----")) {
		return nil, fmt.Errorf("package.linux.apt.public_key %s is not an ASCII-armored public key (gpg --armor --export <id>)", c.PublicKey)
	}
	return key, nil
}

// AptOptions drives `scorix apt`.
type AptOptions struct {
	Dir          string
	ArtifactsDir string
	URL          string
	JSONOut      io.Writer
}

// AptResult reports the repository `scorix apt` wrote.
type AptResult struct {
	Repo     string   `json:"repo,omitempty"`
	URL      string   `json:"url,omitempty"`
	Packages []string `json:"packages,omitempty"`
	Skipped  string   `json:"skipped,omitempty"`
}

// Apt writes a signed apt repository holding every .deb in the artifacts
// directory to <artifacts>/apt. It does nothing when package.linux.apt is unset.
func Apt(ctx context.Context, opt AptOptions) error {
	res := &AptResult{}
	err := apt(ctx, opt, res)
	if opt.JSONOut != nil {
		return EmitJSON(opt.JSONOut, "apt", res, err)
	}
	return err
}

type debEntry struct {
	file    string
	pkg     string
	arch    string
	control string
	pool    string
}

func apt(ctx context.Context, opt AptOptions, res *AptResult) error {
	root, err := filepath.Abs(orDefault(opt.Dir, "."))
	if err != nil {
		return err
	}
	cfg, err := loadProjectConfig(filepath.Join(root, "scorix.yaml"))
	if err != nil {
		return fmt.Errorf("load scorix.yaml: %w", err)
	}
	c := cfg.Package.apt()
	if c == nil {
		res.Skipped = "package.linux.apt is not set"
		fmt.Println("==> no package.linux.apt in scorix.yaml - no apt repository written")
		return nil
	}
	pub, err := readAptPublicKey(root, c)
	if err != nil {
		return err
	}
	url := strings.TrimRight(c.URL, "/")
	if opt.URL != "" && strings.TrimRight(opt.URL, "/") != url {
		return fmt.Errorf("apt: the .deb points apt at %s (package.linux.apt.url) but this repository is for %s", url, opt.URL)
	}
	res.URL = url

	artDir := opt.ArtifactsDir
	if artDir == "" {
		artDir = filepath.Join(root, "artifacts")
	}
	debs, err := filepath.Glob(filepath.Join(artDir, "*.deb"))
	if err != nil {
		return err
	}
	if len(debs) == 0 {
		return fmt.Errorf("apt: no .deb in %s - run `scorix package --target deb` first", artDir)
	}
	sort.Strings(debs)

	key := envVal(aptKeyEnv)
	if key == "" {
		return fmt.Errorf("apt: %s is empty - set it to the ASCII-armored (or base64 of it) private key matching %s", aptKeyEnv, c.PublicKey)
	}

	repo := filepath.Join(artDir, "apt")
	if err := os.RemoveAll(repo); err != nil {
		return err
	}
	entries, err := aptPool(debs, repo, c.component())
	if err != nil {
		return err
	}
	if err := aptIndexes(repo, c, entries, time.Now().UTC()); err != nil {
		return err
	}
	if err := aptSign(ctx, filepath.Join(repo, "dists", c.suite()), key, pub); err != nil {
		_ = os.RemoveAll(repo)
		return err
	}
	res.Repo = repo
	for _, e := range entries {
		res.Packages = append(res.Packages, e.pool)
	}
	fmt.Printf("==> wrote apt repository %s for %s (%d package(s))\n", repo, url, len(entries))
	return nil
}

func aptPool(debs []string, repo, component string) ([]debEntry, error) {
	var entries []debEntry
	for _, f := range debs {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		control, err := debControl(data)
		if err != nil {
			return nil, fmt.Errorf("apt: %s: %w", filepath.Base(f), err)
		}
		pkg, arch := controlField(control, "Package"), controlField(control, "Architecture")
		if pkg == "" || arch == "" {
			return nil, fmt.Errorf("apt: %s: control has no Package or Architecture", filepath.Base(f))
		}
		pool := path.Join("pool", component, pkg[:1], pkg, filepath.Base(f))
		dest := filepath.Join(repo, filepath.FromSlash(pool))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return nil, err
		}
		md, s1, s256 := md5.Sum(data), sha1.Sum(data), sha256.Sum256(data)
		stanza := strings.TrimRight(control, "\n") + fmt.Sprintf("\nFilename: %s\nSize: %d\nMD5sum: %s\nSHA1: %s\nSHA256: %s\n",
			pool, len(data), hex.EncodeToString(md[:]), hex.EncodeToString(s1[:]), hex.EncodeToString(s256[:]))
		entries = append(entries, debEntry{file: f, pkg: pkg, arch: arch, control: stanza, pool: pool})
	}
	return entries, nil
}

func aptIndexes(repo string, c *AptConfig, entries []debEntry, now time.Time) error {
	dists := filepath.Join(repo, "dists", c.suite())
	byArch := map[string][]string{}
	for _, e := range entries {
		byArch[e.arch] = append(byArch[e.arch], e.control)
	}
	archs := slices.Sorted(maps.Keys(byArch))
	type indexFile struct {
		rel  string
		body []byte
	}
	var files []indexFile
	for _, a := range archs {
		body := []byte(strings.Join(byArch[a], "\n"))
		var gz bytes.Buffer
		w, err := gzip.NewWriterLevel(&gz, gzip.BestCompression)
		if err != nil {
			return err
		}
		if _, err := w.Write(body); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
		rel := path.Join(c.component(), "binary-"+a, "Packages")
		files = append(files, indexFile{rel, body}, indexFile{rel + ".gz", gz.Bytes()})
	}
	for _, f := range files {
		dest := filepath.Join(dists, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, f.body, 0o644); err != nil {
			return err
		}
	}

	var release strings.Builder
	label := orDefault(controlField(entries[0].control, "Package"), "scorix")
	fmt.Fprintf(&release, "Origin: %s\nLabel: %s\nSuite: %s\nCodename: %s\nDate: %s\nArchitectures: %s\nComponents: %s\n",
		label, label, c.suite(), c.suite(), now.Format("Mon, 02 Jan 2006 15:04:05 UTC"), strings.Join(archs, " "), c.component())
	release.WriteString("MD5Sum:\n")
	for _, f := range files {
		sum := md5.Sum(f.body)
		fmt.Fprintf(&release, " %s %d %s\n", hex.EncodeToString(sum[:]), len(f.body), f.rel)
	}
	release.WriteString("SHA256:\n")
	for _, f := range files {
		sum := sha256.Sum256(f.body)
		fmt.Fprintf(&release, " %s %d %s\n", hex.EncodeToString(sum[:]), len(f.body), f.rel)
	}
	return os.WriteFile(filepath.Join(dists, "Release"), []byte(release.String()), 0o644)
}

func aptPrivateKey(v string) ([]byte, error) {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "-----BEGIN PGP PRIVATE KEY BLOCK-----") {
		return []byte(v + "\n"), nil
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil || !bytes.Contains(raw, []byte("-----BEGIN PGP PRIVATE KEY BLOCK-----")) {
		return nil, errors.New("apt: the signing key is neither an ASCII-armored private key nor base64 of one")
	}
	return raw, nil
}

func aptSign(ctx context.Context, dists, privateKey string, publicKey []byte) error {
	if !hasTool("gpg") || !hasTool("gpgv") {
		return errors.New("apt: gpg and gpgv are needed to sign the repository")
	}
	key, err := aptPrivateKey(privateKey)
	if err != nil {
		return err
	}
	home, err := os.MkdirTemp("", "scorix-apt-gpg-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(home)
	defer func() {
		if hasTool("gpgconf") {
			_ = exec.Command("gpgconf", "--homedir", home, "--kill", "all").Run()
		}
	}()
	if err := os.Chmod(home, 0o700); err != nil {
		return err
	}
	gpg := func(stdin []byte, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "gpg", append([]string{"--homedir", home, "--batch", "--yes", "--pinentry-mode", "loopback", "--passphrase", ""}, args...)...)
		cmd.Stdin = bytes.NewReader(stdin)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("gpg %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
		}
		return out.Bytes(), nil
	}
	if _, err := gpg(key, "--import"); err != nil {
		return err
	}
	release := filepath.Join(dists, "Release")
	if _, err := gpg(nil, "--digest-algo", "SHA256", "--clearsign", "-o", filepath.Join(dists, "InRelease"), release); err != nil {
		return err
	}
	if _, err := gpg(nil, "--digest-algo", "SHA256", "--armor", "--detach-sign", "-o", release+".gpg", release); err != nil {
		return err
	}

	ring, err := gpg(publicKey, "--dearmor")
	if err != nil {
		return err
	}
	ringPath := filepath.Join(home, "shipped.gpg")
	if err := os.WriteFile(ringPath, ring, 0o600); err != nil {
		return err
	}
	verify := exec.CommandContext(ctx, "gpgv", "--keyring", ringPath, filepath.Join(dists, "InRelease"))
	if out, err := verify.CombinedOutput(); err != nil {
		return fmt.Errorf("apt: the signing key does not match the public key the .deb ships, so apt would refuse every update: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func debControl(deb []byte) (string, error) {
	if !bytes.HasPrefix(deb, []byte("!<arch>\n")) {
		return "", errors.New("not a .deb (no ar header)")
	}
	rest := deb[8:]
	for len(rest) >= 60 {
		hdr := rest[:60]
		name := strings.TrimRight(strings.TrimSpace(string(hdr[:16])), "/")
		var size int
		if _, err := fmt.Sscanf(strings.TrimSpace(string(hdr[48:58])), "%d", &size); err != nil || size < 0 || 60+size > len(rest) {
			return "", errors.New("corrupt ar member header")
		}
		body := rest[60 : 60+size]
		rest = rest[60+size:]
		if size%2 == 1 && len(rest) > 0 {
			rest = rest[1:]
		}
		if name != "control.tar.gz" {
			if strings.HasPrefix(name, "control.tar") {
				return "", fmt.Errorf("%s is not supported, only control.tar.gz", name)
			}
			continue
		}
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		tr := tar.NewReader(zr)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", err
			}
			if strings.TrimPrefix(h.Name, "./") == "control" {
				b, err := io.ReadAll(tr)
				return string(b), err
			}
		}
		return "", errors.New("control.tar.gz has no control file")
	}
	return "", errors.New("no control.tar.gz member")
}

func controlField(control, field string) string {
	for _, line := range strings.Split(control, "\n") {
		if v, ok := strings.CutPrefix(line, field+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
