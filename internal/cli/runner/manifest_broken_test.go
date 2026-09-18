package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandsRefuseAManifestTheyCannotParse(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.com/probe\n\ngo 1.26\n")
	mustWrite(t, filepath.Join(dir, "scorix.yaml"), "name: probe\nproto: idl/app.proto\nthis: [is: not: valid\n")
	mustWrite(t, filepath.Join(dir, "idl", "app.proto"), `syntax = "proto3";
package app;
service system {
  rpc Ping(PingReq) returns (PingResp);
}
message PingReq {}
message PingResp { bool ok = 1; }
`)

	for _, c := range []struct {
		name string
		run  func() error
	}{
		{"generate proto", func() error {
			return GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir, Check: true})
		}},
		{"surface", func() error {
			return Surface(context.Background(), SurfaceOptions{Dir: dir})
		}},
		{"generate model", func() error {
			return GenerateModel(context.Background(), GenerateModelOptions{Dir: dir, Check: true})
		}},
	} {
		err := c.run()
		if err == nil {
			t.Errorf("%s accepted a scorix.yaml it could not parse", c.name)
			continue
		}
		if !strings.Contains(err.Error(), "scorix.yaml") {
			t.Errorf("%s failed without naming the file: %v", c.name, err)
		}
	}
}

// Each falls back silently otherwise: dev loses the build tags, package the declared
// targets, and app control points at the data dir of an app named "".
func TestTheRestOfTheCommandsRefuseAManifestTheyCannotParse(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.com/probe\n\ngo 1.26\n")
	mustWrite(t, filepath.Join(dir, "scorix.yaml"), "name: probe\napp:\n  name: probe\nthis: [is: not: valid\n")

	if err := Dev(context.Background(), DevOptions{Dir: dir}); err == nil || !strings.Contains(err.Error(), "scorix.yaml") {
		t.Errorf("dev on an unparseable manifest = %v", err)
	}
	if _, err := resolvePackageTargets(dir, PackageOptions{}); err == nil || !strings.Contains(err.Error(), "scorix.yaml") {
		t.Errorf("package target resolution on an unparseable manifest = %v", err)
	}
	if _, err := AppControlPath(dir); err == nil || !strings.Contains(err.Error(), "scorix.yaml") {
		t.Errorf("app control path on an unparseable manifest = %v", err)
	}
}

func TestProtoCommandsStillRunWithoutAManifest(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.com/probe\n\ngo 1.26\n")
	mustWrite(t, filepath.Join(dir, "idl", "app.proto"), `syntax = "proto3";
package app;
service system {
  rpc Ping(PingReq) returns (PingResp);
}
message PingReq {}
message PingResp { bool ok = 1; }
`)
	if err := Surface(context.Background(), SurfaceOptions{Dir: dir}); err != nil {
		t.Errorf("surface needs a manifest it never needed before: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err != nil {
		t.Errorf("generate proto needs a manifest it never needed before: %v", err)
	}
}
