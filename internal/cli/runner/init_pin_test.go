package runner

import (
	"strings"
	"testing"
)

func TestScorixRequireNeverNamesAVersionThatDoesNotExist(t *testing.T) {
	const pseudo = "v0.28.1-0.20260916025525-708e7c8e3bc3"
	cases := map[string]struct {
		cliVersion   string
		fromCheckout bool
		want         []string
	}{
		"installed at a tag": {"v0.27.1", false,
			[]string{"mod", "edit", "-require", "github.com/tradalab/scorix@v0.27.1"}},
		"installed at a pre-release tag": {"v0.29.0-rc.1", false,
			[]string{"mod", "edit", "-require", "github.com/tradalab/scorix@v0.29.0-rc.1"}},
		"built from a clean checkout at a tag": {"v0.28.0", true,
			[]string{"mod", "edit", "-require", "github.com/tradalab/scorix@v0.28.0"}},
		"installed from main with scorix upgrade": {pseudo, false,
			[]string{"mod", "edit", "-require", "github.com/tradalab/scorix@" + pseudo}},
		"built from an untagged commit": {pseudo, true,
			[]string{"get", "github.com/tradalab/scorix@latest"}},
		"built from a dirty tree": {pseudo + "+dirty", true,
			[]string{"get", "github.com/tradalab/scorix@latest"}},
		"built from source": {"dev", false,
			[]string{"get", "github.com/tradalab/scorix@latest"}},
		"no build info at all": {"", false,
			[]string{"get", "github.com/tradalab/scorix@latest"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := scorixRequireArgs(scorixPinVersion(c.cliVersion, c.fromCheckout))
			if strings.Join(got, " ") != strings.Join(c.want, " ") {
				t.Fatalf("go %v, want go %v", got, c.want)
			}
			if strings.Contains(strings.Join(got, " "), "v0.0.0") {
				t.Error("scaffold pins a revision that has never existed")
			}
		})
	}
}
