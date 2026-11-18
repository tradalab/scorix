package runner

import (
	"fmt"
	"strings"

	"github.com/tradalab/scorix/idl"
)

var swiftReserved = map[string]bool{
	"Any": true, "Array": true, "Bool": true, "Data": true, "Dictionary": true, "Double": true,
	"Error": true, "Float": true, "Int": true, "Never": true, "Optional": true, "PluginCall": true,
	"Reply": true, "Result": true, "ScorixPlugin": true, "ScorixPlugins": true, "Set": true, "String": true,
}

var swiftKeywords = map[string]bool{
	"as": true, "associatedtype": true, "break": true, "case": true, "catch": true, "class": true,
	"continue": true, "default": true, "defer": true, "deinit": true, "do": true, "else": true,
	"enum": true, "extension": true, "fallthrough": true, "false": true, "fileprivate": true,
	"for": true, "func": true, "guard": true, "if": true, "import": true, "in": true, "init": true,
	"inout": true, "internal": true, "is": true, "let": true, "nil": true, "operator": true,
	"private": true, "protocol": true, "public": true, "repeat": true, "rethrows": true,
	"return": true, "self": true, "Self": true, "static": true, "struct": true, "subscript": true,
	"super": true, "switch": true, "throw": true, "throws": true, "true": true, "try": true,
	"typealias": true, "var": true, "where": true, "while": true,
}

type swiftFile struct {
	Host     bool
	Plugins  []ktPlugin
	Messages []swiftMessage
}

type swiftMessage struct {
	Name   string
	Fields []swiftField
}

type swiftField struct {
	Name, Key, Type, Default, Decode, Encode string
}

func swiftNatives(pf protoFile) (swiftFile, error) {
	kt, err := kotlinNatives(pf, "")
	if err != nil {
		return swiftFile{}, err
	}
	byName := map[string]protoMessage{}
	for _, m := range pf.Messages {
		byName[idl.ExportedName(m.Name)] = m
	}
	out := swiftFile{}
	for _, p := range kt.Plugins {
		sp := ktPlugin{Name: p.Name, Class: p.Class}
		for _, m := range p.Methods {
			m.Name = swiftIdent(m.Wire)
			sp.Methods = append(sp.Methods, m)
		}
		out.Plugins = append(out.Plugins, sp)
	}
	for _, m := range kt.Messages {
		if swiftReserved[m.Name] {
			return swiftFile{}, fmt.Errorf("proto: message %s is used by a @native service, and in Swift that name is taken by a type the generated plugins use; rename the message", m.Name)
		}
		sm := swiftMessage{Name: m.Name}
		for _, f := range byName[m.Name].Fields {
			sm.Fields = append(sm.Fields, swiftFieldOf(f))
		}
		out.Messages = append(out.Messages, sm)
	}
	return out, nil
}

var swiftScalars = map[string][3]string{
	"string":   {"String", `""`, `$ as? String ?? ""`},
	"bool":     {"Bool", "false", `$ as? Bool ?? false`},
	"int32":    {"Int", "0", `($ as? NSNumber)?.intValue ?? 0`},
	"sint32":   {"Int", "0", `($ as? NSNumber)?.intValue ?? 0`},
	"sfixed32": {"Int", "0", `($ as? NSNumber)?.intValue ?? 0`},
	"uint32":   {"Int", "0", `($ as? NSNumber)?.intValue ?? 0`},
	"fixed32":  {"Int", "0", `($ as? NSNumber)?.intValue ?? 0`},
	"int64":    {"Int", "0", `($ as? NSNumber)?.intValue ?? 0`},
	"sint64":   {"Int", "0", `($ as? NSNumber)?.intValue ?? 0`},
	"sfixed64": {"Int", "0", `($ as? NSNumber)?.intValue ?? 0`},
	"uint64":   {"Int", "0", `($ as? NSNumber)?.intValue ?? 0`},
	"fixed64":  {"Int", "0", `($ as? NSNumber)?.intValue ?? 0`},
	"double":   {"Double", "0", `($ as? NSNumber)?.doubleValue ?? 0`},
	"float":    {"Float", "0", `($ as? NSNumber)?.floatValue ?? 0`},
	"bytes":    {"Data", "Data()", `($ as? String).flatMap { Data(base64Encoded: $0) } ?? Data()`},
}

func swiftFieldOf(f protoField) swiftField {
	key := f.JSONName
	typ := idl.ExportedName(f.Type)
	zero := typ + "()"
	decode := typ + `(json: $ as? [String: Any] ?? [:])`
	encode := func(v string) string { return v + ".toJSON()" }
	if s, ok := swiftScalars[f.Type]; ok {
		typ, zero, decode = s[0], s[1], s[2]
		encode = func(v string) string { return v }
		if f.Type == "bytes" {
			encode = func(v string) string { return v + ".base64EncodedString()" }
		}
	}
	name := swiftIdent(idl.LowerCamel(f.Name))
	value := `o["` + key + `"]`
	if f.Repeated {
		elem := strings.ReplaceAll(decode, "$ ", "$0 ")
		enc := name
		if e := encode("$0"); e != "$0" {
			enc = name + ".map { " + e + " }"
		}
		return swiftField{
			Name: name, Key: key, Type: "[" + typ + "]", Default: "[]",
			Decode: "(" + value + ` as? [Any] ?? []).map { ` + elem + ` }`,
			Encode: enc,
		}
	}
	return swiftField{
		Name: name, Key: key, Type: typ, Default: zero,
		Decode: strings.ReplaceAll(decode, "$ ", value+" "),
		Encode: encode(name),
	}
}

func swiftIdent(s string) string {
	if swiftKeywords[s] {
		return "`" + s + "`"
	}
	return s
}
