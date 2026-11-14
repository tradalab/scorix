package systemtray

import (
	"slices"
	"strings"
	"testing"

	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module/moduletest"
)

func TestTitleAndTooltipAreReadOnEveryBuild(t *testing.T) {
	moduletest.Load(t, New(nil), map[string]any{"title": "App", "tooltip": "App"})
	if slices.ContainsFunc(logger.Tail(), func(line string) bool {
		return strings.Contains(line, "modules.systemtray") && strings.Contains(line, "unknown key")
	}) {
		t.Fatalf("title/tooltip reported as unknown: %v", logger.Tail())
	}
}
