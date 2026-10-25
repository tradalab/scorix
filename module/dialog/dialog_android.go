//go:build android

package dialog

import (
	"context"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/internal/hostapi"
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
	out, err := hostapi.Dialog.OpenDocument(ctx, &hostapi.MimeFilter{Mime: mimeOf(req.Ext)})
	if native.IsCode(err, "canceled") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &FileData{Name: out.Name, Data: out.Data}, nil
}

func saveFileData(ctx context.Context, req SaveFileDataRequest) (string, error) {
	out, err := hostapi.Dialog.CreateDocument(ctx, &hostapi.NewDocument{FileName: req.FileName, Mime: mimeOf(req.Ext), Data: req.Data})
	if native.IsCode(err, "canceled") {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return out.Name, nil
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
	out, err := hostapi.Dialog.OpenTree(ctx, &hostapi.Empty{})
	if native.IsCode(err, "canceled") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Folder{ID: out.Id, Name: out.Name}, nil
}

func writeFolderFile(ctx context.Context, req WriteFolderFileRequest) error {
	_, err := hostapi.Dialog.WriteTree(ctx, &hostapi.TreeFile{Tree: req.Folder, Name: req.Name, Mime: mimeOf(req.Ext), Data: req.Data})
	if native.IsCode(err, CodeFolderGone) {
		return fault.New(CodeFolderGone, "the app no longer has access to that folder")
	}
	return err
}

func pickFile(ctx context.Context, req PickFileRequest) (*SavedFile, error) {
	out, err := hostapi.Dialog.PickFile(ctx, &hostapi.NewDocument{FileName: req.FileName, Mime: mimeOf(req.Ext)})
	switch {
	case native.IsCode(err, "canceled"):
		return nil, nil
	case native.IsCode(err, CodeNotPersistable):
		return nil, fault.New(CodeNotPersistable, "that place does not let the app write there again later")
	case err != nil:
		return nil, err
	}
	return &SavedFile{ID: out.Id, Name: out.Name}, nil
}

func rewriteFile(ctx context.Context, req RewriteFileRequest) error {
	_, err := hostapi.Dialog.RewriteFile(ctx, &hostapi.FileRewrite{Uri: req.File, Data: req.Data})
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
	_, err := hostapi.Dialog.Message(ctx, &hostapi.MessageBox{Title: req.Title, Text: req.Text})
	return err
}
