package runner

import (
	"encoding/json"
	"regexp"
)

// The strictest client rule on record (Claude's): anything else is refused at
// connect time, far from the proto that caused it.
var mcpToolNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Under the JSON names the generated types decode, so what a client sends is what
// reg unmarshals. closed is for a request: those types drop a misspelt field.
func mcpSchema(pf protoFile, message string, closed bool) (string, error) {
	return schemaOf(pf, message, closed, func(f protoField) string { return f.JSONName })
}

func schemaOf(pf protoFile, message string, closed bool, key func(protoField) string) (string, error) {
	byName := make(map[string]protoMessage, len(pf.Messages))
	for _, m := range pf.Messages {
		byName[m.Name] = m
	}
	b, err := json.Marshal(messageSchema(byName, message, closed, map[string]bool{}, key))
	return string(b), err
}

func messageSchema(byName map[string]protoMessage, name string, closed bool, open map[string]bool, key func(protoField) string) map[string]any {
	msg, ok := byName[name]
	// A message already being described is recursive: stop at "an object", which names
	// no properties and so cannot be closed either.
	if !ok || open[name] {
		return map[string]any{"type": "object"}
	}
	open[name] = true
	defer delete(open, name)
	props := make(map[string]any, len(msg.Fields))
	for _, f := range msg.Fields {
		s, ok := fieldElem(f)
		if !ok {
			s = messageSchema(byName, f.Type, closed, open, key)
		}
		props[key(f)] = orNull(f, shaped(f, s))
	}
	schema := map[string]any{"type": "object", "properties": props}
	if closed {
		schema["additionalProperties"] = false
	}
	return schema
}

func fieldElem(f protoField) (map[string]any, bool) {
	if f.Enum != nil {
		values := make([]any, len(f.Enum))
		for i, v := range f.Enum {
			values[i] = v
		}
		return map[string]any{"anyOf": []any{map[string]any{"type": "string", "enum": values}, map[string]any{"type": "integer"}}}, true
	}
	return scalarSchema(f.Type)
}

func shaped(f protoField, elem map[string]any) map[string]any {
	switch {
	case f.Map:
		s := map[string]any{"type": "object", "additionalProperties": elem}
		if pattern, ok := mapKeyPatterns[f.MapKey]; ok {
			s["propertyNames"] = map[string]any{"pattern": pattern}
		}
		return s
	case f.Repeated:
		return map[string]any{"type": "array", "items": elem}
	}
	return elem
}

var mapKeyPatterns = map[string]string{
	"int32": signedKey, "sint32": signedKey, "sfixed32": signedKey, "int64": signedKey, "sint64": signedKey, "sfixed64": signedKey,
	"uint32": unsignedKey, "fixed32": unsignedKey, "uint64": unsignedKey, "fixed64": unsignedKey,
	"bool": "^(true|false)$",
}

const (
	signedKey   = "^-?[0-9]+$"
	unsignedKey = "^[0-9]+$"
)

func orNull(f protoField, s map[string]any) map[string]any {
	if !f.Optional {
		return s
	}
	return map[string]any{"anyOf": []any{s, map[string]any{"type": "null"}}}
}

func scalarSchema(typ string) (map[string]any, bool) {
	switch typ {
	case "string":
		return map[string]any{"type": "string"}, true
	case "bool":
		return map[string]any{"type": "boolean"}, true
	case "double", "float":
		return map[string]any{"type": "number"}, true
	case "int32", "sint32", "sfixed32", "uint32", "fixed32", "int64", "sint64", "sfixed64", "uint64", "fixed64":
		return map[string]any{"type": "integer"}, true
	case "bytes":
		return map[string]any{"type": "string", "contentEncoding": "base64"}, true
	}
	return nil, false
}
