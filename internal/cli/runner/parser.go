package runner

import (
	"strings"

	"github.com/tradalab/scorix/idl"
)

func parseProto(src string) (protoFile, error) {
	return idl.ParseApp("app.proto", src)
}

func protoTypeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, ".")
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

func typeRef(name string) string {
	tn := protoTypeName(name)
	if tn == "Empty" {
		return "types.Empty"
	}
	if tn == "any" {
		return "any"
	}
	return "types." + idl.ExportedName(tn)
}

func tsTypeRef(name string) string {
	if protoTypeName(name) == "Empty" {
		return "Empty"
	}
	return idl.ExportedName(protoTypeName(name))
}
