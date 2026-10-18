// Package dialog provides native OS dialogs (file/dir picker, message box) via
// ncruces/zenity on desktop and the system picker on Android.
package dialog

import (
	"context"
	"fmt"
	"strings"

	"github.com/tradalab/scorix/fault"

	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
)

type Config struct {
	Enabled bool `json:"enabled"`
}

type DialogModule struct {
	ctx *module.Context
	cfg Config
}

func New() *DialogModule {
	return &DialogModule{}
}

func (m *DialogModule) Name() string    { return "dialog" }
func (m *DialogModule) Version() string { return "1.0.0" }

func (m *DialogModule) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[dialog] loading (v%s)", m.Version()))
	m.ctx = ctx

	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}

	module.Expose(m, "OpenFile", ctx.IPC)
	module.Expose(m, "OpenDirectory", ctx.IPC)
	module.Expose(m, "SaveFile", ctx.IPC)
	module.Expose(m, "Message", ctx.IPC)
	module.Expose(m, "OpenFileData", ctx.IPC)
	module.Expose(m, "SaveFileData", ctx.IPC)
	module.Expose(m, "OpenFolder", ctx.IPC)
	module.Expose(m, "WriteFolderFile", ctx.IPC)
	module.Expose(m, "PickFile", ctx.IPC)
	module.Expose(m, "RewriteFile", ctx.IPC)

	return nil
}

func (m *DialogModule) OnStart() error  { return nil }
func (m *DialogModule) OnStop() error   { return nil }
func (m *DialogModule) OnUnload() error { return nil }

type OpenFileRequest struct {
	Title  string `json:"title"`
	Filter string `json:"filter"`
	Ext    string `json:"ext"`
}

// JS: scorix.invoke("mod:dialog:OpenFile", { title: "Select File", filter: "Text Files", ext: "txt" })
// On Android the file is copied into the app cache: os.Open cannot read a content URI.
func (m *DialogModule) OpenFile(ctx context.Context, req OpenFileRequest) (string, error) {
	return openFile(ctx, req)
}

type OpenDirectoryRequest struct {
	Title string `json:"title"`
	Dir   string `json:"dir,omitempty"`
}

// JS: scorix.invoke("mod:dialog:OpenDirectory", { title: "Pick folder", dir: "/Users/foo" })
// Not on Android: a picked tree is a content URI with no path behind it.
func (m *DialogModule) OpenDirectory(ctx context.Context, req OpenDirectoryRequest) (string, error) {
	return openDirectory(ctx, req)
}

type SaveFileRequest struct {
	Title    string `json:"title"`
	Filter   string `json:"filter"`
	Ext      string `json:"ext"`
	FileName string `json:"fileName,omitempty"`
	Dir      string `json:"dir,omitempty"`
}

// JS: scorix.invoke("mod:dialog:SaveFile", { title, filter, ext, fileName, dir })
// Not on Android, where a content URI has no path to write: use SaveFileData.
func (m *DialogModule) SaveFile(ctx context.Context, req SaveFileRequest) (string, error) {
	return saveFile(ctx, req)
}

type MessageRequest struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	Level string `json:"level"` // info, error
}

// JS: scorix.invoke("mod:dialog:Message", { title: "Alert", text: "Something happened", level: "info" })
func (m *DialogModule) Message(ctx context.Context, req MessageRequest) (string, error) {
	if err := message(ctx, req); err != nil {
		return "", err
	}
	return "ok", nil
}

type OpenFileDataRequest struct {
	Title  string `json:"title"`
	Filter string `json:"filter"`
	Ext    string `json:"ext"`
}

// FileData carries the bytes themselves, the only form that works with Android content URIs.
type FileData struct {
	Name string `json:"name"`
	Data []byte `json:"data"` // base64 over the JSON envelope
}

// JS: scorix.invoke("mod:dialog:OpenFileData", { title, filter, ext }) -> {name, data} | null on cancel
func (m *DialogModule) OpenFileData(ctx context.Context, req OpenFileDataRequest) (*FileData, error) {
	return openFileData(ctx, req)
}

type SaveFileDataRequest struct {
	Title    string `json:"title"`
	Filter   string `json:"filter"`
	Ext      string `json:"ext"`
	FileName string `json:"fileName,omitempty"`
	Data     []byte `json:"data"`
}

// JS: scorix.invoke("mod:dialog:SaveFileData", { title, ext, fileName, data }) -> name written, "" on cancel
func (m *DialogModule) SaveFileData(ctx context.Context, req SaveFileDataRequest) (string, error) {
	return saveFileData(ctx, req)
}

type OpenFolderRequest struct {
	Title string `json:"title"`
}

// Folder.ID is a path on desktop and a tree URI with a persisted grant on
// Android, so treat it as opaque.
type Folder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// JS: scorix.invoke("mod:dialog:OpenFolder", { title }) -> {id, name} | null on cancel
func (m *DialogModule) OpenFolder(ctx context.Context, req OpenFolderRequest) (*Folder, error) {
	return openFolder(ctx, req)
}

type WriteFolderFileRequest struct {
	Folder string `json:"folder"` // Folder.ID
	Name   string `json:"name"`
	Ext    string `json:"ext"`
	Data   []byte `json:"data"`
}

// CodeFolderGone also covers an Android grant the user revoked: either way the app must ask again.
const CodeFolderGone = "folder_gone"

// JS: scorix.invoke("mod:dialog:WriteFolderFile", { folder, name, ext, data })
func (m *DialogModule) WriteFolderFile(ctx context.Context, req WriteFolderFileRequest) (string, error) {
	if req.Folder == "" || req.Name == "" || strings.ContainsAny(req.Name, `/\`) {
		return "", fault.New("invalid_request", "folder and a plain file name are required")
	}
	if err := writeFolderFile(ctx, req); err != nil {
		return "", err
	}
	return "ok", nil
}

type PickFileRequest struct {
	Title    string `json:"title"`
	FileName string `json:"fileName"`
	Ext      string `json:"ext"`
}

// SavedFile.ID is a path on desktop and a document URI with a persisted grant
// on Android, so treat it as opaque.
type SavedFile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CodeNotPersistable: some document providers grant write access now but not later.
const CodeNotPersistable = "not_persistable"

// JS: scorix.invoke("mod:dialog:PickFile", { title, fileName, ext }) -> {id, name} | null on cancel
// Exists beside OpenFolder because Google Drive on Android offers a single file but no folder tree.
func (m *DialogModule) PickFile(ctx context.Context, req PickFileRequest) (*SavedFile, error) {
	if req.FileName == "" || strings.ContainsAny(req.FileName, `/\`) {
		return nil, fault.New("invalid_request", "a plain file name is required")
	}
	return pickFile(ctx, req)
}

type RewriteFileRequest struct {
	File string `json:"file"` // SavedFile.ID
	Data []byte `json:"data"`
}

// JS: scorix.invoke("mod:dialog:RewriteFile", { file, data })
// On Android a provider that refuses truncating writes can leave an older tail
// after a shorter body, so store content that says where it ends (JSON does).
func (m *DialogModule) RewriteFile(ctx context.Context, req RewriteFileRequest) (string, error) {
	if req.File == "" {
		return "", fault.New("invalid_request", "a file from PickFile is required")
	}
	if err := rewriteFile(ctx, req); err != nil {
		return "", err
	}
	return "ok", nil
}

func (m *DialogModule) Capability() string { return "shell" }
