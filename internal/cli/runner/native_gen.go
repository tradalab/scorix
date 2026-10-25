package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Declared in or imported into the generated file's package, where a message of the same name
// would shadow them and break every use.
var ktReserved = map[string]bool{
	"Any": true, "Boolean": true, "ByteArray": true, "Context": true, "Double": true, "Float": true,
	"Int": true, "JSONArray": true, "JSONObject": true, "List": true, "Long": true, "Map": true,
	"PluginCall": true, "Reply": true, "ScorixPlugin": true, "ScorixPlugins": true, "String": true,
	"Unit": true, "Base64": true,
}

// Kotlin side of the @native services, rendered by codegen/android/plugins.kt.
type ktFile struct {
	AppPackage string
	Plugins    []ktPlugin
	Messages   []ktMessage
	Base64     bool
	Arrays     bool
}

type ktPlugin struct {
	Name    string // the wire name Go calls, the service's lowerCamel
	Class   string // the app's implementation; the generated base adds "Base"
	Methods []ktMethod
}

type ktMethod struct {
	Name, Wire, Doc, Req, Res string
}

type ktMessage struct {
	Name   string
	Fields []ktField
}

type ktField struct {
	Name, Key, Type, Default, Decode, Encode string
}

func kotlinNatives(pf protoFile, appPackage string) (ktFile, error) {
	out := ktFile{AppPackage: ktPackage(appPackage)}
	bases := map[string]bool{}
	for _, svc := range pf.Natives {
		bases[exportedName(svc.Name)+"PluginBase"] = true
	}
	byName := map[string]protoMessage{}
	for _, m := range pf.Messages {
		byName[m.Name] = m
	}
	need := map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if need[name] {
			return nil
		}
		need[name] = true
		m, ok := byName[name]
		if !ok {
			if name == "Empty" {
				return nil
			}
			return fmt.Errorf("proto: message %s is used by a @native service but never declared", name)
		}
		for _, f := range m.Fields {
			if _, scalar := ktScalars[f.Type]; !scalar {
				if err := visit(f.Type); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, svc := range pf.Natives {
		p := ktPlugin{Name: svc.Package, Class: exportedName(svc.Name) + "Plugin"}
		for _, rpc := range svc.RPCs {
			for _, t := range []string{rpc.RequestType, rpc.ResponseType} {
				if err := visit(t); err != nil {
					return ktFile{}, err
				}
			}
			p.Methods = append(p.Methods, ktMethod{
				Name: ktIdent(rpc.NativeMethod),
				Wire: rpc.NativeMethod,
				// Kotlin block comments nest, so an opening /* in the text would swallow the file too.
				Doc: strings.NewReplacer("*/", "* /", "/*", "/ *").Replace(rpc.Doc),
				Req: exportedName(rpc.RequestType),
				Res: exportedName(rpc.ResponseType),
			})
		}
		out.Plugins = append(out.Plugins, p)
	}
	var names []string
	for n := range need {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if ktReserved[exportedName(n)] || bases[exportedName(n)] {
			return ktFile{}, fmt.Errorf("proto: message %s is used by a @native service, and in Kotlin that name is taken by a type the generated plugins use; rename the message", n)
		}
		msg := ktMessage{Name: exportedName(n)}
		for _, f := range byName[n].Fields {
			kf := ktFieldOf(f)
			out.Base64 = out.Base64 || f.Type == "bytes"
			out.Arrays = out.Arrays || f.Repeated
			msg.Fields = append(msg.Fields, kf)
		}
		out.Messages = append(out.Messages, msg)
	}
	return out, nil
}

// Proto scalar -> Kotlin type, zero value, JSONObject getter and JSONArray getter. uint32 is a
// Long because it can pass Int's ceiling.
var ktScalars = map[string][4]string{
	"string":   {"String", `""`, "optString", "getString"},
	"bool":     {"Boolean", "false", "optBoolean", "getBoolean"},
	"int32":    {"Int", "0", "optInt", "getInt"},
	"sint32":   {"Int", "0", "optInt", "getInt"},
	"sfixed32": {"Int", "0", "optInt", "getInt"},
	"uint32":   {"Long", "0L", "optLong", "getLong"},
	"fixed32":  {"Long", "0L", "optLong", "getLong"},
	"int64":    {"Long", "0L", "optLong", "getLong"},
	"sint64":   {"Long", "0L", "optLong", "getLong"},
	"sfixed64": {"Long", "0L", "optLong", "getLong"},
	"uint64":   {"Long", "0L", "optLong", "getLong"},
	"fixed64":  {"Long", "0L", "optLong", "getLong"},
	"double":   {"Double", "0.0", "optDouble", "getDouble"},
	"float":    {"Float", "0f", "optDouble", "getDouble"},
	"bytes":    {"ByteArray", "ByteArray(0)", "optString", "getString"},
}

// Keys are the JSON names Go's generated types use, so both sides read one shape.
func ktFieldOf(f protoField) ktField {
	key := f.JSONName
	elemType, elemDefault := exportedName(f.Type), exportedName(f.Type)+"()"
	decodeElem := func(a string) string { return exportedName(f.Type) + ".fromJson(" + a + ".getJSONObject(it))" }
	encodeElem := func(v string) string { return v + ".toJson()" }
	one := func() string {
		return exportedName(f.Type) + `.fromJson(o.optJSONObject("` + key + `") ?: JSONObject())`
	}
	if s, ok := ktScalars[f.Type]; ok {
		elemType, elemDefault = s[0], s[1]
		decodeElem = func(a string) string { return a + "." + s[3] + "(it)" }
		encodeElem = func(v string) string { return v }
		one = func() string { return `o.` + s[2] + `("` + key + `")` }
		switch f.Type {
		case "double":
			one = func() string { return `o.optDouble("` + key + `", 0.0)` }
		case "float":
			one = func() string { return `o.optDouble("` + key + `", 0.0).toFloat()` }
			decodeElem = func(a string) string { return a + ".getDouble(it).toFloat()" }
			encodeElem = func(v string) string { return v + ".toDouble()" }
		case "bytes":
			// encoding/json sends []byte as standard base64.
			// A nil []byte arrives as null, which optString would hand over as the text "null".
			one = func() string {
				return `if (o.isNull("` + key + `")) ByteArray(0) else Base64.decode(o.optString("` + key + `"), Base64.DEFAULT)`
			}
			decodeElem = func(a string) string { return "Base64.decode(" + a + ".getString(it), Base64.DEFAULT)" }
			encodeElem = func(v string) string { return "Base64.encodeToString(" + v + ", Base64.NO_WRAP)" }
		}
	}
	name := ktIdent(lowerCamel(f.Name))
	if f.Repeated {
		return ktField{
			Name:    name,
			Key:     key,
			Type:    "List<" + elemType + ">",
			Default: "emptyList()",
			// Go sends a nil slice as null, which optJSONArray also answers with null.
			Decode: `o.optJSONArray("` + key + `")?.let { a -> List(a.length()) { ` + decodeElem("a") + ` } } ?: emptyList()`,
			Encode: "JSONArray().apply { " + name + ".forEach { put(" + encodeElem("it") + ") } }",
		}
	}
	return ktField{Name: name, Key: key, Type: elemType, Default: elemDefault, Decode: one(), Encode: encodeElem(name)}
}

var ktKeywords = map[string]bool{
	"as": true, "break": true, "class": true, "continue": true, "do": true, "else": true, "false": true,
	"for": true, "fun": true, "if": true, "in": true, "interface": true, "is": true, "null": true,
	"object": true, "package": true, "return": true, "super": true, "this": true, "throw": true,
	"true": true, "try": true, "typealias": true, "typeof": true, "val": true, "var": true,
	"when": true, "while": true,
}

// An application ID may hold a Kotlin keyword segment (in.example.app), which needs backticks.
func ktPackage(id string) string {
	parts := strings.Split(id, ".")
	for i, p := range parts {
		parts[i] = ktIdent(p)
	}
	return strings.Join(parts, ".")
}

func ktIdent(s string) string {
	if ktKeywords[s] {
		return "`" + s + "`"
	}
	return s
}

// The host's own plugins, which an app plugin of the same name replaces (ScorixHost.register).
var hostPlugins = map[string]bool{
	"keystore": true, "biometric": true, "clipboard": true, "dialog": true,
	"scanner": true, "browser": true, "share": true, "bluetooth": true,
}

// Warnings, not errors: replacing a built-in plugin is allowed (auth/app does it), and an
// app made before scorix.gradle builds without the plugins until its gradle file applies it.
func warnNativeSetup(root string, natives []protoService) {
	for _, svc := range natives {
		if hostPlugins[svc.Package] {
			fmt.Printf("warning: @native service %s replaces the host's own %q plugin; the scorix module of that name now reaches yours\n", svc.Name, svc.Package)
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "android", "app", "build.gradle.kts"))
	if err == nil && !strings.Contains(string(b), ".scorix/scorix.gradle") {
		fmt.Println("warning: android/app/build.gradle.kts does not apply .scorix/scorix.gradle, so src/scorix/kotlin is not compiled and every @native call will fail; add apply(from = rootProject.file(\".scorix/scorix.gradle\"))")
	}
}
