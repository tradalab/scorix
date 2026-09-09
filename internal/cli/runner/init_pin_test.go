package runner

import (
	"strings"
	"testing"
)

// The scaffold used to name github.com/tradalab/scorix@v0.0.0 and leave it to
// `go mod tidy`, which cannot resolve a revision that does not exist. Every app
// scaffolded outside this monorepo failed at its first go command; inside it,
// the sibling replace resolved the module locally and hid the whole thing.
func TestScorixRequireNeverNamesAVersionThatDoesNotExist(t *testing.T) {
	cases := map[string]struct {
		cliVersion string
		want       []string
	}{
		"installed at a tag": {"v0.27.1",
			[]string{"mod", "edit", "-require", "github.com/tradalab/scorix@v0.27.1"}},
		"built from source": {"dev",
			[]string{"get", "github.com/tradalab/scorix@latest"}},
		"no build info at all": {"",
			[]string{"get", "github.com/tradalab/scorix@latest"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := scorixRequireArgs(scorixPinVersion(c.cliVersion))
			if strings.Join(got, " ") != strings.Join(c.want, " ") {
				t.Fatalf("go %v, want go %v", got, c.want)
			}
			if strings.Contains(strings.Join(got, " "), "v0.0.0") {
				t.Error("scaffold pins a revision that has never existed")
			}
		})
	}
}
