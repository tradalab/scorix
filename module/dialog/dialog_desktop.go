//go:build !android

package dialog

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/ncruces/zenity"

	"github.com/tradalab/scorix/fault"
)

func fileFilters(name, ext string) []zenity.Option {
	if name == "" && ext == "" {
		return nil
	}
	pattern := "*"
	if ext != "" {
		pattern = "*." + ext
	}
	return []zenity.Option{zenity.FileFilters{{Name: name, Patterns: []string{pattern}, CaseFold: true}}}
}

// Maps user-Cancel to ("", nil) rather than an error (module contract).
func canceled(path string, err error) (string, error) {
	if errors.Is(err, zenity.ErrCanceled) {
		return "", nil
	}
	return path, err
}

func openFile(_ context.Context, req OpenFileRequest) (string, error) {
	opts := []zenity.Option{zenity.Title(req.Title)}
	opts = append(opts, fileFilters(req.Filter, req.Ext)...)
	return canceled(zenity.SelectFile(opts...))
}

func openDirectory(_ context.Context, req OpenDirectoryRequest) (string, error) {
	opts := []zenity.Option{zenity.Title(req.Title), zenity.Directory()}
	if req.Dir != "" {
		opts = append(opts, zenity.Filename(req.Dir))
	}
	return canceled(zenity.SelectFile(opts...))
}

func openFolder(ctx context.Context, req OpenFolderRequest) (*Folder, error) {
	dir, err := openDirectory(ctx, OpenDirectoryRequest{Title: req.Title})
	if err != nil || dir == "" {
		return nil, err
	}
	return &Folder{ID: dir, Name: filepath.Base(dir)}, nil
}

// Temp file plus rename so a sync client watching the folder never uploads half a file.
func writeFolderFile(_ context.Context, req WriteFolderFileRequest) error {
	if st, err := os.Stat(req.Folder); err != nil || !st.IsDir() {
		return fault.New(CodeFolderGone, "the folder is gone: "+req.Folder)
	}
	path := filepath.Join(req.Folder, req.Name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, req.Data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Created up front, as Android's save screen does, so RewriteFile can read a missing file as deleted.
func pickFile(ctx context.Context, req PickFileRequest) (*SavedFile, error) {
	path, err := saveFile(ctx, SaveFileRequest{Title: req.Title, Ext: req.Ext, FileName: req.FileName})
	if err != nil || path == "" {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return &SavedFile{ID: path, Name: filepath.Base(path)}, nil
}

func rewriteFile(_ context.Context, req RewriteFileRequest) error {
	st, err := os.Stat(req.File)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fault.New(CodeFolderGone, "the file is gone: "+req.File)
	case err != nil:
		return err
	case st.IsDir():
		return fault.New("invalid_request", "not a file: "+req.File)
	}
	dir, name := filepath.Split(req.File)
	tmp, err := os.CreateTemp(dir, "."+name+"-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(req.Data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp.Name(), st.Mode().Perm())); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), req.File); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

func saveFile(_ context.Context, req SaveFileRequest) (string, error) {
	opts := []zenity.Option{zenity.Title(req.Title), zenity.ConfirmOverwrite()}
	opts = append(opts, fileFilters(req.Filter, req.Ext)...)
	if req.Dir != "" || req.FileName != "" {
		opts = append(opts, zenity.Filename(filepath.Join(req.Dir, req.FileName)))
	}
	return canceled(zenity.SelectFileSave(opts...))
}

func message(_ context.Context, req MessageRequest) error {
	var err error
	if req.Level == "error" {
		err = zenity.Error(req.Text, zenity.Title(req.Title))
	} else {
		err = zenity.Info(req.Text, zenity.Title(req.Title))
	}
	if errors.Is(err, zenity.ErrCanceled) {
		return nil
	}
	return err
}

func openFileData(ctx context.Context, req OpenFileDataRequest) (*FileData, error) {
	path, err := openFile(ctx, OpenFileRequest{Title: req.Title, Filter: req.Filter, Ext: req.Ext})
	if err != nil || path == "" {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return &FileData{Name: filepath.Base(path), Data: data}, nil
}

func saveFileData(ctx context.Context, req SaveFileDataRequest) (string, error) {
	path, err := saveFile(ctx, SaveFileRequest{Title: req.Title, Filter: req.Filter, Ext: req.Ext, FileName: req.FileName})
	if err != nil || path == "" {
		return "", err
	}
	if err := os.WriteFile(path, req.Data, 0o644); err != nil {
		return "", err
	}
	return filepath.Base(path), nil
}
