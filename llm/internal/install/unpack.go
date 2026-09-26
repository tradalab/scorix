package install

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Every name in an archive is someone else's input: an entry or a link that
// points outside dir is refused, not written.
func Unpack(archive, dir string) error {
	switch {
	case strings.HasSuffix(archive, ".zip"):
		return unzip(archive, dir)
	case strings.HasSuffix(archive, ".tar.gz"):
		return untar(archive, dir)
	}
	return fmt.Errorf("install: %s is not an archive this knows", filepath.Base(archive))
}

func target(dir, name string) (string, error) {
	clean := filepath.FromSlash(path.Clean(strings.TrimPrefix(name, "./")))
	if !filepath.IsLocal(clean) {
		return "", fmt.Errorf("install: archive entry %q leaves the install", name)
	}
	return filepath.Join(dir, clean), nil
}

func unzip(archive, dir string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		dst, err := target(dir, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			continue
		}
		in, err := f.Open()
		if err != nil {
			return err
		}
		if f.Mode()&fs.ModeSymlink != 0 {
			// A zip carries a symlink as an entry whose content is the target,
			// and a tarball's links are checked, so these have to be too.
			link, err := io.ReadAll(io.LimitReader(in, 4<<10))
			in.Close()
			if err != nil {
				return err
			}
			if err := symlink(f.Name, string(link), dst); err != nil {
				return err
			}
			continue
		}
		err = write(dst, in, f.Mode(), int64(f.UncompressedSize64))
		in.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func untar(archive, dir string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		dst, err := target(dir, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := write(dst, tr, h.FileInfo().Mode(), h.Size); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// The Linux and macOS builds link libfoo.so to libfoo.so.0.1.2 in
			// the same directory.
			if err := symlink(h.Name, h.Linkname, dst); err != nil {
				return err
			}
		}
	}
}

// A link out of the install is how an archive gets a later write to land
// somewhere else. Archive names are slash paths: on Windows "/etc/passwd" is
// not absolute to filepath, yet a link to it resolves on the current drive.
func symlink(name, link, dst string) error {
	joined := path.Join(path.Dir(strings.TrimPrefix(name, "./")), link)
	if path.IsAbs(link) || filepath.IsAbs(link) || strings.ContainsAny(link, `\:`) || !filepath.IsLocal(filepath.FromSlash(joined)) {
		return fmt.Errorf("install: link %q -> %q leaves the install", name, link)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Symlink(link, dst)
}

// The mode keeps the executable bit, without which llama-server does not run
// on Linux or macOS. size is what the archive said the entry holds: a zip
// reader only notices a stream longer than that when it reaches the end, by
// which time the disk is already full.
func write(dst string, r io.Reader, mode os.FileMode, size int64) (err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm()|0o600)
	if err != nil {
		return err
	}
	// Whatever landed before the failure is not the file the archive named, and
	// Unpack is called on its own as well as through Assets, which is the only
	// caller that clears the directory afterwards.
	defer func() {
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	n, err := io.Copy(out, io.LimitReader(r, size))
	if err != nil {
		out.Close()
		return err
	}
	// The rest, which for a well-formed entry is nothing: archive/zip checks an
	// entry's CRC only once its reader reaches the end, so a copy that stops at
	// the declared size never asks for the read that would report a flipped
	// bit. A stored entry is not compressed either, so nothing else notices -
	// and a release asset from an older tag carries no sha256 of its own.
	extra, err := io.Copy(io.Discard, r)
	if err != nil {
		out.Close()
		return err
	}
	if n != size || extra != 0 {
		out.Close()
		return fmt.Errorf("install: %s holds %d bytes, the archive said %d", filepath.Base(dst), n+extra, size)
	}
	return out.Close()
}
