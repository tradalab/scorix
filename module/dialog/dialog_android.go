//go:build android

package dialog

import (
	"context"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/native"
)

// Parameters stripped: the picker matches the whole string, so "; charset=utf-8" greys out every file.
func mimeOf(ext string) string {
	if ext == "" {
		return ""
	}
	t, _, _ := strings.Cut(mime.TypeByExtension("."+ext), ";")
	return strings.TrimSpace(t)
}

func openFileData(ctx context.Context, req OpenFileDataRequest) (*FileData, error) {
	var out FileData
	err := native.Call(ctx, "dialog", "openDocument", map[string]any{"mime": mimeOf(req.Ext)}, &out)
	if native.IsCode(err, "canceled") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func saveFileData(ctx context.Context, req SaveFileDataRequest) (string, error) {
	var out struct {
		Name string `json:"name"`
	}
	err := native.Call(ctx, "dialog", "createDocument", map[string]any{
		"fileName": req.FileName,
		"mime":     mimeOf(req.Ext),
		"data":     req.Data,
	}, &out)
	if native.IsCode(err, "canceled") {
		return "", nil
	}
	return out.Name, err
}

func openFile(ctx context.Context, req OpenFileRequest) (string, error) {
	f, err := openFileData(ctx, OpenFileDataRequest{Title: req.Title, Filter: req.Filter, Ext: req.Ext})
	if err != nil || f == nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "picked-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, filepath.Base(f.Name))
	if err := os.WriteFile(path, f.Data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func openFolder(ctx context.Context, req OpenFolderRequest) (*Folder, error) {
	var out Folder
	err := native.Call(ctx, "dialog", "openTree", nil, &out)
	if native.IsCode(err, "canceled") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func writeFolderFile(ctx context.Context, req WriteFolderFileRequest) error {
	err := native.Call(ctx, "dialog", "writeTree", map[string]any{
		"tree": req.Folder,
		"name": req.Name,
		"mime": mimeOf(req.Ext),
		"data": req.Data,
	}, nil)
	if native.IsCode(err, CodeFolderGone) {
		return fault.New(CodeFolderGone, "the app no longer has access to that folder")
	}
	return err
}

func pickFile(ctx context.Context, req PickFileRequest) (*SavedFile, error) {
	var out SavedFile
	err := native.Call(ctx, "dialog", "pickFile", map[string]any{"fileName": req.FileName, "mime": mimeOf(req.Ext)}, &out)
	switch {
	case native.IsCode(err, "canceled"):
		return nil, nil
	case native.IsCode(err, CodeNotPersistable):
		return nil, fault.New(CodeNotPersistable, "that place does not let the app write there again later")
	case err != nil:
		return nil, err
	}
	return &out, nil
}

func rewriteFile(ctx context.Context, req RewriteFileRequest) error {
	err := native.Call(ctx, "dialog", "rewriteFile", map[string]any{"uri": req.File, "data": req.Data}, nil)
	if native.IsCode(err, CodeFolderGone) {
		return fault.New(CodeFolderGone, "the app no longer has access to that file")
	}
	return err
}

func openDirectory(context.Context, OpenDirectoryRequest) (string, error) {
	return "", fault.New(fault.CodeUnavailable, "OpenDirectory is not available on Android: a picked folder has no path")
}

func saveFile(context.Context, SaveFileRequest) (string, error) {
	return "", fault.New(fault.CodeUnavailable, "SaveFile is not available on Android: use SaveFileData")
}

func message(ctx context.Context, req MessageRequest) error {
	return native.Call(ctx, "dialog", "message", map[string]any{"title": req.Title, "text": req.Text, "level": req.Level}, nil)
}
