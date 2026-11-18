package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const swiftProto = `syntax = "proto3";
package demo;

// @native
service Vault {
  rpc Default(Item) returns (Item);
}

message Item {
  string in = 1;
  bytes blob = 2;
  repeated bytes blobs = 3;
  repeated Tag tags = 4;
  int64 size_ms = 5;
}

message Tag {
  string label = 1;
}
`

func TestSwiftEscapesKeywordsAndReadsEveryFieldShapeFromTheGoJSONKey(t *testing.T) {
	pf, err := hostProto(swiftProto)
	if err != nil {
		t.Fatal(err)
	}
	sw, err := swiftNatives(pf)
	if err != nil {
		t.Fatal(err)
	}
	if got := sw.Plugins[0].Methods[0].Name; got != "`default`" {
		t.Errorf("method name = %s, want the keyword escaped", got)
	}
	var item swiftMessage
	for _, m := range sw.Messages {
		if m.Name == "Item" {
			item = m
		}
	}
	want := map[string][2]string{
		"`in`":   {`o["in"] as? String ?? ""`, "`in`"},
		"blob":   {`(o["blob"] as? String).flatMap { Data(base64Encoded: $0) } ?? Data()`, "blob.base64EncodedString()"},
		"blobs":  {`(o["blobs"] as? [Any] ?? []).map { ($0 as? String).flatMap { Data(base64Encoded: $0) } ?? Data() }`, "blobs.map { $0.base64EncodedString() }"},
		"tags":   {`(o["tags"] as? [Any] ?? []).map { Tag(json: $0 as? [String: Any] ?? [:]) }`, "tags.map { $0.toJSON() }"},
		"sizeMs": {`(o["size_ms"] as? NSNumber)?.intValue ?? 0`, "sizeMs"},
	}
	if len(item.Fields) != len(want) {
		t.Fatalf("fields = %+v", item.Fields)
	}
	for _, f := range item.Fields {
		w, ok := want[f.Name]
		if !ok || f.Decode != w[0] || f.Encode != w[1] {
			t.Errorf("%s: decode %q encode %q, want %q %q", f.Name, f.Decode, f.Encode, w[0], w[1])
		}
	}
}

func TestSwiftRefusesAMessageNamedLikeATypeThePluginsUse(t *testing.T) {
	pf, err := hostProto(strings.ReplaceAll(swiftProto, "Tag", "Data"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := swiftNatives(pf); err == nil || !strings.Contains(err.Error(), "in Swift that name is taken") {
		t.Fatalf("a message named Data: %v", err)
	}
}

func TestAnAppWithAnIOSProjectGetsSwiftBasesForItsNativeServicesAndCheckSeesThem(t *testing.T) {
	dir := newNativeProject(t, nativeProto)
	if err := os.MkdirAll(filepath.Join(dir, "ios", "App"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	sw := readGenerated(t, dir, "ios/App/Scorix/ScorixPlugins.swift")
	for _, want := range []string{
		"import ScorixHost",
		"PhotoPlugin(),",
		"PushPlugin(),",
		"protocol PhotoPluginBase: ScorixPlugin {",
		"    /// Take opens the camera app.\n    func take(_ req: Shot, reply: Reply<Saved>)",
		`case "take": take(Shot(json: args), reply: Reply(call) { $0.toJSON() })`,
		"var others: [Shot] = []",
	} {
		if !strings.Contains(sw, want) {
			t.Errorf("ScorixPlugins.swift lacks %q:\n%s", want, sw)
		}
	}
	if strings.Contains(sw, "Notes") {
		t.Error("a service without @native reached Swift")
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir, Check: true}); err != nil {
		t.Fatalf("check right after generate: %v", err)
	}
}

func TestAnAppWithoutAnIOSProjectGetsNoSwift(t *testing.T) {
	dir := newNativeProject(t, nativeProto)
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ios")); !os.IsNotExist(err) {
		t.Fatalf("ios/ appeared in an app that has none: %v", err)
	}
}
