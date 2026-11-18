// Package ioswebview is the iOS driver: the Swift host in mobile/ios/ScorixHost owns the
// process, UIKit and the WKWebView, and reaches Go through the functions exported here.
package ioswebview

import "strings"

func sameURL(u string) string { return u }

func fromWebURL(u string) (string, bool) { return u, strings.Contains(u, "://") }

func originOf(u string) string {
	scheme, rest, ok := strings.Cut(u, "://")
	if !ok {
		return ""
	}
	host, _, _ := strings.Cut(rest, "/")
	return strings.ToLower(scheme) + "://" + strings.ToLower(host)
}
