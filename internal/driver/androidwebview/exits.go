package androidwebview

import (
	"strings"

	"github.com/tradalab/scorix/internal/origin"
	"github.com/tradalab/scorix/logger"
)

type exits struct {
	guard    origin.Guard
	external func(url string)
}

var inPage = map[string]bool{"about": true, "javascript": true, "data": true}

var web = map[string]bool{"http": true, "https": true, "file": true, "mailto": true, "blob": true}

func (e *exits) keepsOut(target string, clicked bool) bool {
	scheme, _, _ := strings.Cut(strings.ToLower(target), ":")
	if inPage[scheme] || web[scheme] && !e.guard.KeepsOut(target) {
		return false
	}
	if e.guard.FirstRefusal(target) {
		logger.Warn("androidwebview: kept a navigation out of the WebView, it leaves the origin the app loaded", "origin", origin.Of(target))
	}
	if clicked && e.external != nil {
		go e.external(target)
	}
	return true
}
