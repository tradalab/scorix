package androidwebview

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestALibraryActivityLockedToAnOrientationDoesNotMakeThatScreenRequiredOnPlay(t *testing.T) {
	raw, err := os.ReadFile("../../../mobile/android/scorix/src/main/AndroidManifest.xml")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`android:screenOrientation="(\w+)"`).FindAllSubmatch(raw, -1) {
		screen := ""
		switch o := strings.ToLower(string(m[1])); {
		case strings.Contains(o, "portrait"):
			screen = "portrait"
		case strings.Contains(o, "landscape"):
			screen = "landscape"
		default:
			continue
		}
		feature := regexp.MustCompile(`<uses-feature\s+android:name="android\.hardware\.screen\.` + screen + `"\s+android:required="false"\s*/>`)
		if !feature.Match(raw) {
			t.Fatalf("an activity is locked to %s but android.hardware.screen.%s is not declared required=false; Play then hides the app from tablets, desktops and XR", m[1], screen)
		}
	}
}
