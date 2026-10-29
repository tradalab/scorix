package runner

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestDedupeMounts(t *testing.T) {
	got := dedupeMounts([]dockerMount{
		{Path: "/w/scorix", ReadOnly: true},
		{Path: "/w/tuvi/app"},
		{Path: "/w/tuvi/app/.scorix/bin/scorix", ReadOnly: true},
		{Path: "/w/scorix-other", ReadOnly: true},
		{Path: "/w/scorix/mobile", ReadOnly: true},
		{Path: "/gomod"},
		{Path: "/gomod/golang.org/toolchain@v0.0.1", ReadOnly: true},
	})
	want := []dockerMount{
		{Path: "/gomod"},
		{Path: "/w/scorix", ReadOnly: true},
		{Path: "/w/scorix-other", ReadOnly: true},
		{Path: "/w/tuvi/app"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("dedupeMounts = %v, want %v", got, want)
	}
	if got := dedupeMounts([]dockerMount{{Path: "/w", ReadOnly: true}, {Path: "/w/app"}}); !slices.Equal(got, []dockerMount{{Path: "/w"}}) {
		t.Fatalf("a writable child must keep its parent writable, got %v", got)
	}
}

func TestBindSpecQuotesCommas(t *testing.T) {
	if got := bindSpec("/w/a:b", "/w/a:b", true); got != "type=bind,source=/w/a:b,target=/w/a:b,readonly" {
		t.Errorf("colon path: %s", got)
	}
	if got := bindSpec("/w/a,b", "/opt/x", false); got != `type=bind,"source=/w/a,b",target=/opt/x` {
		t.Errorf("comma path: %s", got)
	}
}

func TestDockerRunArgsKeepSecretsOffTheCommandLine(t *testing.T) {
	t.Setenv("SCORIX_ANDROID_KEYSTORE_PASSWORD", "hunter2")
	d := dockerRun{Image: "img", UID: 1000, GID: 1000, Mounts: []dockerMount{{Path: "/w/app"}, {Path: "/go", ReadOnly: true}},
		SDK: "/c/android-sdk", Cache: "/c", GOROOT: "/go", ModCache: "/gomod", Dir: "/w/app",
		Pass: []string{"GOPROXY=https://u:token@proxy.example"}}
	args := d.args("scorix", "mobile")
	line := strings.Join(args, " ")
	if strings.Contains(line, "hunter2") || strings.Contains(line, "token") {
		t.Fatalf("password on the command line: %s", line)
	}
	for _, want := range []string{"--mount type=bind,source=/w/app,target=/w/app ", "--mount type=bind,source=/go,target=/go,readonly ",
		"--mount type=bind,source=/c/android-sdk,target=/opt/android-sdk ", "-e GOMODCACHE=/gomod ",
		"-e SCORIX_ANDROID_KEYSTORE_PASSWORD ", "-e GOPROXY ", "-e PATH=/go/bin:", "-w /w/app img scorix mobile"} {
		if !strings.Contains(line, want) {
			t.Errorf("args lack %q: %s", want, line)
		}
	}
	if strings.Contains(line, "-e SCORIX_ANDROID_KEY_ALIAS") {
		t.Errorf("unset variable passed through: %s", line)
	}
}

func TestReadInnerResult(t *testing.T) {
	var res MobileResult
	ok := `{"command":"mobile build","ok":true,"exit":0,"data":{"platform":"android","output":"/w/app/artifacts/a.apk"}}`
	if err := readInnerResult([]byte(ok), nil, &res); err != nil || res.Output != "/w/app/artifacts/a.apk" {
		t.Fatalf("ok envelope: %v, %+v", err, res)
	}

	failed := `{"command":"mobile build","ok":false,"exit":4,"kind":"missing_prerequisite","error":"no gradle"}`
	var ee *ExitError
	if err := readInnerResult([]byte(failed), errors.New("exit status 4"), &res); !errors.As(err, &ee) || ee.Code != ExitMissing || ee.Kind != "missing_prerequisite" {
		t.Fatalf("failed envelope must keep the inner exit status, got %v", err)
	}

	if err := readInnerResult(nil, errors.New("exit status 125"), &res); err == nil || !strings.Contains(err.Error(), "125") {
		t.Fatalf("no envelope must report the docker error, got %v", err)
	}
}
