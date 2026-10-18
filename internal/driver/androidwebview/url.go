// Package androidwebview drives the system WebView as a c-shared library; the Kotlin
// host in mobile/android/scorix owns the process and UI thread and calls in over JNI.
package androidwebview

import (
	"net/url"
	"strings"
)

// WebView cannot register custom schemes, so scorix://app/x is served as
// https://app.scorix.localhost/x: never hits the network, still a secure origin.
const localSuffix = ".localhost"

func toWebURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !isAppScheme(u.Scheme) {
		return raw
	}
	host := u.Scheme + localSuffix
	if u.Host != "" {
		host = u.Host + "." + host
	}
	u.Scheme, u.Host = "https", host
	return u.String()
}

func fromWebURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.HasSuffix(u.Host, localSuffix) {
		return "", false
	}
	labels := strings.TrimSuffix(u.Host, localSuffix)
	host, scheme := "", labels
	if i := strings.LastIndexByte(labels, '.'); i >= 0 {
		host, scheme = labels[:i], labels[i+1:]
	}
	if !isAppScheme(scheme) {
		return "", false
	}
	u.Scheme, u.Host = scheme, host
	return u.String(), true
}

func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// The scheme becomes a DNS label in the host, so it must fit one.
func isAppScheme(s string) bool {
	switch s {
	case "", "http", "https", "about", "data", "blob", "file", "javascript":
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}
