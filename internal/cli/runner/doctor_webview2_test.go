package runner

import "testing"

func TestParseChromiumMajor(t *testing.T) {
	cases := map[string]struct {
		major int
		ok    bool
	}{
		"120.0.2210.91": {120, true},
		"107.0.1418.24": {107, true},
		"99.0.1150.55":  {99, true},
		"120":           {120, true},
		"Installer":     {0, false},
		"":              {0, false},
		"1x2.0":         {0, false},
		".120":          {0, false},
	}
	for in, want := range cases {
		got, ok := parseChromiumMajor(in)
		if ok != want.ok || got != want.major {
			t.Errorf("parseChromiumMajor(%q) = %d, %v; want %d, %v", in, got, ok, want.major, want.ok)
		}
	}
}

func TestWebView2FloorIsTheShellTarget(t *testing.T) {
	if minWebView2Major != 107 {
		t.Fatalf("floor is %d: raise it only with the scaffold's browser target, and update ARCHITECTURE.md §2.3", minWebView2Major)
	}
}
