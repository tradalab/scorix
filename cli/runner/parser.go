package runner

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

func parseProto(src string) (protoFile, error) {
	clean := stripProtoComments(src)
	pf := protoFile{
		Package:       "app",
		HasMiddleware: false,
	}

	// Extract middleware names from @middleware annotations
	middlewareRe := regexp.MustCompile(`@middleware\s+([A-Za-z_][A-Za-z0-9_]*)`)
	middlewareMap := make(map[string]bool)
	for _, m := range middlewareRe.FindAllStringSubmatch(src, -1) {
		name := m[1]
		if !middlewareMap[name] {
			middlewareMap[name] = true
			pf.Middlewares = append(pf.Middlewares, name)
		}
		pf.HasMiddleware = true
	}

	if m := regexp.MustCompile(`(?m)\bpackage\s+([A-Za-z_][A-Za-z0-9_.]*)\s*;`).FindStringSubmatch(clean); len(m) == 2 {
		parts := strings.Split(m[1], ".")
		pf.Package = parts[len(parts)-1]
	}

	messageRe := regexp.MustCompile(`(?s)\bmessage\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{(.*?)\}`)
	for _, m := range messageRe.FindAllStringSubmatch(clean, -1) {
		if m[1] == "Empty" {
			pf.HasEmpty = true
		}
		msg := protoMessage{Name: m[1], GoName: exportedName(m[1]), Fields: parseProtoFields(m[2])}
		pf.Messages = append(pf.Messages, msg)
	}

	serviceRe := regexp.MustCompile(`(?s)((?://.*?\n|/\*.*?\*/\s*)*)\bservice\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{(.*?)\}`)
	// Match: optional comment block + rpc keyword + name + signature
	rpcWithCommentRe := regexp.MustCompile(`(?s)((?:[ \t]*//[^\n]*\n)*)[ \t]*\brpc\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(\s*([A-Za-z_][A-Za-z0-9_.]*)\s*\)\s*returns\s*\(\s*([A-Za-z_][A-Za-z0-9_.]*)\s*\)`)
	for _, m := range serviceRe.FindAllStringSubmatch(src, -1) {
		svc := protoService{Name: m[2]}
		// Extract middlewares from service-level comments
		for _, mm := range middlewareRe.FindAllStringSubmatch(m[1], -1) {
			svc.Middlewares = append(svc.Middlewares, mm[1])
		}

		for _, r := range rpcWithCommentRe.FindAllStringSubmatch(m[3], -1) {
			commentBlock := r[1]
			rpcName := r[2]
			reqType := protoTypeName(r[3])
			respType := protoTypeName(r[4])

			// Extract per-RPC middlewares from comment block
			var rpcMiddlewares []string
			for _, mm := range middlewareRe.FindAllStringSubmatch(commentBlock, -1) {
				rpcMiddlewares = append(rpcMiddlewares, mm[1])
			}

			svc.RPCs = append(svc.RPCs, protoRPC{
				Name:         rpcName,
				RequestType:  reqType,
				ResponseType: respType,
				Middlewares:  rpcMiddlewares, // empty = inherit from service in generate.go
			})
		}
		if len(svc.RPCs) == 0 {
			return pf, fmt.Errorf("service %s has no rpc methods", svc.Name)
		}
		pf.Services = append(pf.Services, svc)
	}

	sort.Slice(pf.Messages, func(i, j int) bool {
		return pf.Messages[i].Name < pf.Messages[j].Name
	})
	return pf, nil
}

func stripProtoComments(src string) string {
	lineRe := regexp.MustCompile(`(?m)//.*$`)
	blockRe := regexp.MustCompile(`(?s)/\*.*?\*/`)
	return lineRe.ReplaceAllString(blockRe.ReplaceAllString(src, ""), "")
}

func parseProtoFields(body string) []protoField {
	fieldRe := regexp.MustCompile(`(?m)^\s*(repeated\s+)?([A-Za-z_][A-Za-z0-9_.<>]*)\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*\d+`)
	var fields []protoField
	for _, m := range fieldRe.FindAllStringSubmatch(body, -1) {
		name := m[3]
		repeated := strings.TrimSpace(m[1]) != ""
		goType := protoScalarToGo(protoTypeName(m[2]))
		tsType := protoScalarToTS(protoTypeName(m[2]))
		if repeated {
			goType = "[]" + goType
			tsType = tsType + "[]"
		}
		fields = append(fields, protoField{
			Name:     name,
			JSONName: snakeName(name),
			GoName:   exportedName(name),
			Type:     protoTypeName(m[2]),
			GoType:   goType,
			TSType:   tsType,
			Repeated: repeated,
		})
	}
	return fields
}

func protoTypeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, ".")
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

func protoScalarToGo(name string) string {
	switch name {
	case "double":
		return "float64"
	case "float":
		return "float32"
	case "int32", "sint32", "sfixed32":
		return "int32"
	case "int64", "sint64", "sfixed64":
		return "int64"
	case "uint32", "fixed32":
		return "uint32"
	case "uint64", "fixed64":
		return "uint64"
	case "bool":
		return "bool"
	case "string":
		return "string"
	case "bytes":
		return "[]byte"
	case "Empty":
		return "Empty"
	default:
		return exportedName(name)
	}
}

func protoScalarToTS(name string) string {
	switch name {
	case "double", "float", "int32", "sint32", "sfixed32", "int64", "sint64", "sfixed64", "uint32", "fixed32", "uint64", "fixed64":
		return "number"
	case "bool":
		return "boolean"
	case "string":
		return "string"
	case "bytes":
		return "Uint8Array"
	case "Empty":
		return "Record<string, never>"
	default:
		return exportedName(name)
	}
}

func typeRef(name string) string {
	tn := protoTypeName(name)
	if tn == "Empty" {
		return "types.Empty"
	}
	if tn == "any" {
		return "any"
	}
	return "types." + exportedName(tn)
}

func tsTypeRef(name string) string {
	if protoTypeName(name) == "Empty" {
		return "Empty"
	}
	return exportedName(protoTypeName(name))
}
