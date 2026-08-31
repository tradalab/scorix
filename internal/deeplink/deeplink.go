package deeplink

import (
	"os"
	"path/filepath"
	"strings"
)

// Classify splits args into declared-scheme URLs and existing files with a
// declared extension. Anything else (flags, unrelated paths) is dropped: argv
// on a protocol launch also carries whatever the OS or shell added.
func Classify(args, schemes, exts []string) (urls, files []string) {
	normSchemes := make([]string, 0, len(schemes))
	for _, s := range schemes {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			normSchemes = append(normSchemes, s+"://")
		}
	}
	normExts := make([]string, 0, len(exts))
	for _, e := range exts {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			if !strings.HasPrefix(e, ".") {
				e = "." + e
			}
			normExts = append(normExts, e)
		}
	}

	for _, arg := range args {
		a := strings.TrimSpace(arg)
		if a == "" {
			continue
		}
		lower := strings.ToLower(a)
		matched := false
		for _, s := range normSchemes {
			if strings.HasPrefix(lower, s) {
				urls = append(urls, a)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		for _, e := range normExts {
			if strings.HasSuffix(lower, e) && filepath.IsAbs(a) {
				if info, err := os.Lstat(a); err == nil && !info.IsDir() {
					files = append(files, filepath.Clean(a))
				}
				break
			}
		}
	}
	return urls, files
}
