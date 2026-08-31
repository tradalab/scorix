//go:build !windows

package deeplink

// Registration on macOS (Info.plist CFBundleURLTypes/CFBundleDocumentTypes) and
// Linux (.desktop MimeType + %u) belongs to the packaged bundle, not runtime.
func Register(identifier, appName, exe string, schemes, exts []string) error {
	return nil
}
