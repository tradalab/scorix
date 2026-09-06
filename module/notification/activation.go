package notification

import (
	"net/url"
	"strings"
)

// activationHost separates a notification click from the app's own deep links.
const activationHost = "notify"

// DefaultAction is the key reported when the user clicks the notification body
// rather than one of its buttons.
const DefaultAction = "default"

// activationURL is what the OS is told to open when a notification is clicked.
// Routing a click through the app's OWN registered scheme is what buys a
// callback without a COM activator on Windows or a D-Bus service on Linux: the
// OS launches the app, single-instance forwards the argv, and the existing
// deep-link path delivers it.
func activationURL(scheme, id, action string) string {
	if scheme == "" || id == "" {
		return ""
	}
	q := url.Values{"id": {id}}
	if action != "" && action != DefaultAction {
		q.Set("action", action)
	}
	return scheme + "://" + activationHost + "?" + q.Encode()
}

// parseActivation returns ok=false for anything that is not ours.
func parseActivation(raw string) (id, action string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return "", "", false
	}
	// "x://notify?.." lands in Host, "x:notify?.." in Opaque; which one arrives
	// depends on the launcher, and a click that silently does nothing is the
	// worst outcome here.
	host, path := u.Host, strings.Trim(u.Path, "/")
	if host == "" {
		host, path = u.Opaque, ""
	}
	if host == "" {
		host, path = path, ""
	}
	// Only the bare host: "myapp://notify/thread/9?id=5" is the app's own deep
	// link and must reach its handler, not be eaten as a notification click.
	if path != "" || !strings.EqualFold(host, activationHost) {
		return "", "", false
	}
	q := u.Query()
	id = q.Get("id")
	if id == "" {
		return "", "", false
	}
	action = q.Get("action")
	if action == "" {
		action = DefaultAction
	}
	return id, action, true
}
