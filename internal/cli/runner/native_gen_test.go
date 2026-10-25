package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const nativeProto = `syntax = "proto3";
package app;

message Empty {}

message Shot {
  string id = 1;
  bool thumb = 2;
  repeated string tags = 3;
  bytes data = 4;
  float scale = 5;
}

message Saved {
  bool saved = 1;
  Shot shot = 2;
  repeated Shot others = 3;
}

// @native expose
service Photo {
  // Take opens the camera app.
  rpc Take (Shot) returns (Saved);
  rpc Clear (Empty) returns (Empty);
}

// @native
service Push {
  rpc Token (Empty) returns (Shot);
}

service Notes {
  rpc List (Empty) returns (Saved);
}
`

func newNativeProject(t *testing.T, proto string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"go.mod":        "module example.com/demo\n\ngo 1.26\n",
		"scorix.yaml":   "name: example.com/demo\napp:\n  name: demo\n  identifier: com.example.demo\n",
		"idl/app.proto": proto,
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "android", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readGenerated(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGenerateNativeServices(t *testing.T) {
	dir := newNativeProject(t, nativeProto)
	ctx := context.Background()
	if err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}

	host := readGenerated(t, dir, "internal/host/host.go")
	for _, want := range []string{
		`var Photo photoPlugin`,
		`var Push pushPlugin`,
		`func (photoPlugin) Take(ctx context.Context, req *types.Shot) (*types.Saved, error) {`,
		`native.Call(ctx, "photo", "take", req, out)`,
		`return nil, native.Fault(err)`,
		`// Take opens the camera app.`,
	} {
		if !strings.Contains(host, want) {
			t.Errorf("host.go lacks %s:\n%s", want, host)
		}
	}

	handler := readGenerated(t, dir, "internal/handler/handler.go")
	if !strings.Contains(handler, `return scorixhost.Photo.Take(ctx, r)`) || !strings.Contains(handler, `"photo:take"`) {
		t.Errorf("an exposed native service has no pass-through command:\n%s", handler)
	}
	if strings.Contains(handler, "push:") || strings.Contains(handler, "logic/photo") {
		t.Errorf("a native service reached logic or an unexposed one reached the page:\n%s", handler)
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "logic", "photo")); err == nil {
		t.Error("logic stubs were written for a native service")
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "logic", "notes", "list_logic.go")); err != nil {
		t.Error("the plain service lost its logic stub")
	}
	api := readGenerated(t, dir, "shell/api/index.ts")
	if !strings.Contains(api, `take: (params: T.Shot) => scorix.invoke<T.Saved>("photo:take", params)`) || strings.Contains(api, "push") {
		t.Errorf("shell/api: %s", api)
	}

	kt := readGenerated(t, dir, "android/app/src/scorix/kotlin/dev/scorix/app/ScorixPlugins.kt")
	for _, want := range []string{
		`com.example.demo.PhotoPlugin(context),`,
		`com.example.demo.PushPlugin(context),`,
		`abstract class PhotoPluginBase : ScorixPlugin {`,
		`final override val name = "photo"`,
		`abstract fun take(req: Shot, reply: Reply<Saved>)`,
		`"take" -> take(Shot.fromJson(args), Reply(call) { it.toJson() })`,
		`val tags: List<String> = emptyList(),`,
		`tags = o.optJSONArray("tags")?.let { a -> List(a.length()) { a.getString(it) } } ?: emptyList(),`,
		`data = if (o.isNull("data")) ByteArray(0) else Base64.decode(o.optString("data"), Base64.DEFAULT),`,
		`.put("data", Base64.encodeToString(data, Base64.NO_WRAP))`,
		`scale = o.optDouble("scale", 0.0).toFloat(),`,
		`.put("scale", scale.toDouble())`,
		`shot = Shot.fromJson(o.optJSONObject("shot") ?: JSONObject()),`,
		`others = o.optJSONArray("others")?.let { a -> List(a.length()) { Shot.fromJson(a.getJSONObject(it)) } } ?: emptyList(),`,
		"class Empty {",
	} {
		if !strings.Contains(kt, want) {
			t.Errorf("ScorixPlugins.kt lacks %s", want)
		}
	}
	if t.Failed() {
		t.Log(kt)
	}

	if err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir, Check: true}); err != nil {
		t.Fatalf("check right after generate: %v", err)
	}
}

func TestNativeAnnotationMisuse(t *testing.T) {
	for name, proto := range map[string]string{
		"modifier": "message Empty {}\n// @native public\nservice P {\n  rpc A (Empty) returns (Empty);\n}\n",
		"on rpc":   "message Empty {}\nservice P {\n  // @native\n  rpc A (Empty) returns (Empty);\n}\n",
		"stream":   "message Empty {}\n// @native\nservice P {\n  rpc A (Empty) returns (stream Empty);\n}\n",
		"event":    "message Empty {}\n// @native\nservice P {\n  // @event\n  rpc A (Empty) returns (Empty);\n}\n",
		"reserved": "message Empty {}\n// @native\nservice P {\n  rpc OnStart (Empty) returns (Empty);\n}\n",
	} {
		dir := newNativeProject(t, proto)
		if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err == nil {
			t.Errorf("%s: generate accepted it", name)
		}
	}
}

// Enabled built-ins in scorix.yaml are registered by the generated handler, not by main.go.
func TestGenerateRegistersEnabledModules(t *testing.T) {
	dir := newNativeProject(t, nativeProto)
	yaml := "name: example.com/demo\napp:\n  name: demo\n  identifier: com.example.demo\nmodules:\n  share:\n    enabled: true\n  http:\n    enabled: true\n    allow: [\"a.example\"]\n  clipboard:\n    enabled: false\n  llm:\n    enabled: true\n"
	if err := os.WriteFile(filepath.Join(dir, "scorix.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	mods := readGenerated(t, dir, "internal/handler/modules.go")
	for _, want := range []string{`a.DefaultModule(httpfetch.New())`, `a.DefaultModule(share.New())`} {
		if !strings.Contains(mods, want) {
			t.Errorf("modules.go lacks %s:\n%s", want, mods)
		}
	}
	for _, not := range []string{"clipboard.New", "llm.New"} {
		if strings.Contains(mods, not) {
			t.Errorf("modules.go registers %s, which is off or takes arguments:\n%s", not, mods)
		}
	}
	if !strings.Contains(readGenerated(t, dir, "internal/handler/handler.go"), "registerModules(a)") {
		t.Error("RegisterHandlers does not call registerModules")
	}
}

func TestNativeKotlinNamesThatWouldBreak(t *testing.T) {
	shadow := "message List {\n  string id = 1;\n}\n// @native\nservice P {\n  rpc A (List) returns (List);\n}\n"
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: newNativeProject(t, shadow)}); err == nil {
		t.Error("a message named List was generated into the plugins file, where it shadows kotlin.collections.List")
	}

	proto := "message Empty {}\n// @native expose\nservice Cam {\n  // Snap reads /* globs.\n  rpc Snap (Empty) returns (Empty);\n}\nservice Host {\n  rpc Info (Empty) returns (Empty);\n}\n"
	dir := newNativeProject(t, proto)
	yaml := "name: example.com/demo\napp:\n  name: demo\n  identifier: in.example.demo\n"
	if err := os.WriteFile(filepath.Join(dir, "scorix.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	kt := readGenerated(t, dir, "android/app/src/scorix/kotlin/dev/scorix/app/ScorixPlugins.kt")
	if !strings.Contains(kt, "`in`.example.demo.CamPlugin(context),") {
		t.Error("a keyword segment of the application ID was not escaped")
	}
	if strings.Contains(kt, "reads /* globs") {
		t.Error("an opening /* in a doc comment reached the Kotlin, where block comments nest")
	}
	handler := readGenerated(t, dir, "internal/handler/handler.go")
	if !strings.Contains(handler, `scorixhost "example.com/demo/internal/host"`) || !strings.Contains(handler, `"example.com/demo/internal/logic/host"`) {
		t.Errorf("a service named Host collides with the native client import:\n%s", handler)
	}
}
