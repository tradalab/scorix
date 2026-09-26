// Package install puts a runtime's release build on disk the same way for
// every runtime: verified download, unpack beside, rename into place.
package install

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tradalab/scorix/llm/model"
)

// Downloads the named release assets, unpacks them together into dir and
// returns the path of exe inside. dir is either a whole install or absent:
// assets are unpacked beside it and renamed in only once exe is found.
func Assets(ctx context.Context, store *model.Store, src model.Source, snap *model.Snapshot, assets []string, dir, exe string, progress func(model.Progress)) (string, error) {
	if found, err := Find(dir, exe); err == nil {
		return found, nil
	}
	var files []model.File
	for _, a := range assets {
		i := slices.IndexFunc(snap.Files, func(f model.File) bool { return f.Path == a })
		if i < 0 {
			return "", fmt.Errorf("install: %s@%s has no %s", snap.Repo, snap.Revision, a)
		}
		files = append(files, snap.Files[i])
	}
	archives, err := store.Get(ctx, src, snap, files, progress)
	if err != nil {
		return "", err
	}
	partial, old := dir+PartialSuffix, dir+AsideSuffix
	if err := os.RemoveAll(partial); err != nil {
		return "", err
	}
	// A leak from a run that could not remove it - Windows refuses to delete a
	// directory holding a file something still has open - swept here, because
	// it sits beside the install and a caller globbing for installs sees it.
	_ = os.RemoveAll(old)
	for _, a := range archives {
		if err := Unpack(a, partial); err != nil {
			os.RemoveAll(partial)
			return "", err
		}
	}
	if _, err := Find(partial, exe); err != nil {
		os.RemoveAll(partial)
		return "", fmt.Errorf("install: %s: %w", assets[0], err)
	}
	// A reinstall - after antivirus quarantined the binary, or someone deleted
	// it - finds the directory already there, and a rename onto a directory
	// that exists is refused on Windows even when it is empty. Moved aside
	// rather than removed, so a rename that fails still leaves an install.
	if err := os.Rename(dir, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		os.RemoveAll(partial)
		return "", err
	}
	if err := os.Rename(partial, dir); err != nil {
		_ = os.Rename(old, dir)
		os.RemoveAll(partial)
		return "", err
	}
	_ = os.RemoveAll(old)
	// Unpacked, the archives are only disk space.
	for _, a := range archives {
		os.Remove(a)
	}
	return Find(dir, exe)
}

// The names Assets works under while it installs. They sit beside the install
// directory, so a caller that finds installs with a glob has to skip them -
// they match the same pattern and sort after the real one.
const (
	PartialSuffix = ".partial"
	AsideSuffix   = ".old"
)

// Scratch reports whether dir is one of Assets' own working directories rather
// than an install.
func Scratch(dir string) bool {
	b := filepath.Base(dir)
	return strings.HasSuffix(b, PartialSuffix) || strings.HasSuffix(b, AsideSuffix)
}

// The Windows zips hold an executable at the top, the tarballs one directory
// down, and whisper.cpp's under Release; searching beats knowing each layout.
func Find(dir, name string) (string, error) {
	var found string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == name {
			found = p
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", errors.New("no " + name + " in the build")
	}
	return found, nil
}
