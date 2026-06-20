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

	middlewareRe := regexp.MustCompile(`@middleware\s+([A-Za-z_][A-Za-z0-9_]*)`)
	// @event [in|out] marks an rpc as a one-way event (default direction: out)
	eventRe := regexp.MustCompile(`@event\b(?:[ \t]+(in|out))?`)
	// @broadcast is the v2 spelling of a callerless Go->JS push (== @event out)
	broadcastRe := regexp.MustCompile(`@broadcast\b`)

	// A trailing same-line @event/@broadcast would be captured as the NEXT rpc's
	// leading comment and silently reclassify it — reject it loudly. Annotation must
	// END the comment line so prose like `// see @event-bus design` isn't matched.
	trailingAnno := regexp.MustCompile(`returns\s*\(\s*(?:stream\s+)?[A-Za-z_][A-Za-z0-9_.]*\s*\)[ \t]*;?[ \t]*//[^\n]*@(?:event|broadcast)(?:[ \t]+(?:in|out))?[ \t]*(?:\r?\n|$)`)
	if m := trailingAnno.FindString(src); m != "" {
		return pf, fmt.Errorf("proto: @event/@broadcast must be on its own comment line above the rpc, not a trailing comment: %q", strings.TrimSpace(m))
	}

	// Collect @middleware only from service-header/rpc scopes (below), not a global
	// scan — else one in an unrelated message comment leaks into every service.
	middlewareMap := make(map[string]bool)
	addMiddleware := func(name string) {
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

	// Header regexes locate the opening; the body is brace-balance scanned so a
	// nested `{}` doesn't truncate at the first inner `}`.
	messageHeadRe := regexp.MustCompile(`\bmessage\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{`)
	for _, loc := range messageHeadRe.FindAllStringSubmatchIndex(clean, -1) {
		name := clean[loc[2]:loc[3]]
		openBrace := loc[1] - 1
		body, ok := braceBalancedBody(clean, openBrace)
		if !ok {
			continue
		}
		if name == "Empty" {
			pf.HasEmpty = true
		}
		msg := protoMessage{Name: name, GoName: exportedName(name), Fields: parseProtoFields(name, body)}
		pf.Messages = append(pf.Messages, msg)
	}

	// Line-comment alt must be //[^\n]*\n, not //.*?\n: under (?s) a non-greedy .*?
	// backtracks across newlines, swallowing non-comment lines into the comment prefix
	// and pulling an unrelated @middleware into the service.
	serviceHeadRe := regexp.MustCompile(`(?s)((?://[^\n]*\n|/\*.*?\*/\s*)*)\bservice\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{`)
	// Optional `stream` before the request and/or response type selects the arity.
	rpcWithCommentRe := regexp.MustCompile(`(?s)((?:[ \t]*//[^\n]*\n)*)[ \t]*\brpc\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(\s*(stream\s+)?([A-Za-z_][A-Za-z0-9_.]*)\s*\)\s*returns\s*\(\s*(stream\s+)?([A-Za-z_][A-Za-z0-9_.]*)\s*\)`)
	for _, loc := range serviceHeadRe.FindAllStringSubmatchIndex(src, -1) {
		commentPrefix := src[loc[2]:loc[3]]
		svc := protoService{Name: src[loc[4]:loc[5]]}
		openBrace := loc[1] - 1
		body, ok := braceBalancedBody(src, openBrace)
		if !ok {
			continue
		}
		for _, mm := range middlewareRe.FindAllStringSubmatch(commentPrefix, -1) {
			svc.Middlewares = append(svc.Middlewares, mm[1])
			addMiddleware(mm[1])
		}

		for _, r := range rpcWithCommentRe.FindAllStringSubmatch(body, -1) {
			commentBlock := r[1]
			rpcName := r[2]
			reqStream := strings.TrimSpace(r[3]) != ""
			reqType := protoTypeName(r[4])
			respStream := strings.TrimSpace(r[5]) != ""
			respType := protoTypeName(r[6])

			arity := "unary"
			switch {
			case !reqStream && respStream:
				arity = "server-stream"
			case reqStream && !respStream:
				arity = "client-stream"
			case reqStream && respStream:
				arity = "bidi"
			}

			var rpcMiddlewares []string
			for _, mm := range middlewareRe.FindAllStringSubmatch(commentBlock, -1) {
				rpcMiddlewares = append(rpcMiddlewares, mm[1])
				addMiddleware(mm[1])
			}

			rpc := protoRPC{
				Name:           rpcName,
				RequestType:    reqType,
				ResponseType:   respType,
				Middlewares:    rpcMiddlewares, // empty = inherit from service in generate.go
				Arity:          arity,
				IsServerStream: arity == "server-stream",
			}
			// bare @event/@broadcast → Go->JS push (out); @event in → JS->Go.
			if m := eventRe.FindStringSubmatch(commentBlock); m != nil {
				rpc.IsEvent = true
				rpc.EventDir = "out"
				if m[1] == "in" {
					rpc.EventDir = "in"
				}
			} else if broadcastRe.MatchString(commentBlock) {
				rpc.IsEvent = true
				rpc.EventDir = "out"
			}
			svc.RPCs = append(svc.RPCs, rpc)
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

// braceBalancedBody returns the text between the '{' at openBrace and its matching
// '}', tracking depth so an inner `{}` doesn't terminate early. ok is false on
// non-'{' or never-balanced (truncated source).
func braceBalancedBody(s string, openBrace int) (body string, ok bool) {
	if openBrace < 0 || openBrace >= len(s) || s[openBrace] != '{' {
		return "", false
	}
	depth := 0
	for i := openBrace; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[openBrace+1 : i], true
			}
		}
	}
	return "", false
}

func stripProtoComments(src string) string {
	lineRe := regexp.MustCompile(`(?m)//.*$`)
	blockRe := regexp.MustCompile(`(?s)/\*.*?\*/`)
	return lineRe.ReplaceAllString(blockRe.ReplaceAllString(src, ""), "")
}

func parseProtoFields(msgName, body string) []protoField {
	fieldRe := regexp.MustCompile(`(?m)^\s*(repeated\s+)?([A-Za-z_][A-Za-z0-9_.<>]*)\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*\d+`)
	var fields []protoField
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		// `}` from nested balanced bodies can leak in here.
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || trimmed == "}" {
			continue
		}
		m := fieldRe.FindStringSubmatch(line)
		if m == nil {
			// Warn so a typo'd field isn't silently dropped.
			fmt.Printf("warning: message %s: skipping unparseable field line: %s\n", msgName, trimmed)
			continue
		}
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
