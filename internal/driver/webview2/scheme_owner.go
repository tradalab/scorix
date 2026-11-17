package webview2

import "strings"

func schemeOwns(scheme, uri string) bool {
	return len(uri) > len(scheme) && uri[len(scheme)] == ':' && strings.EqualFold(uri[:len(scheme)], scheme)
}
