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
	byName := make(map[string]protoMessage, len(pf.Messages))
	for _, m := range pf.Messages {
		byName[m.Name] = m
	}
	b, err := json.Marshal(messageSchema(byName, message, closed, map[string]bool{}))
	return string(b), err
}

func messageSchema(byName map[string]protoMessage, name string, closed bool, open map[string]bool) map[string]any {
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
		s := fieldSchema(byName, f.Type, closed, open)
		if f.Repeated {
			s = map[string]any{"type": "array", "items": s}
		}
		props[f.JSONName] = s
	}
	schema := map[string]any{"type": "object", "properties": props}
	if closed {
		schema["additionalProperties"] = false
	}
	return schema
}

func fieldSchema(byName map[string]protoMessage, typ string, closed bool, open map[string]bool) map[string]any {
	switch typ {
	case "string":
		return map[string]any{"type": "string"}
	case "bool":
		return map[string]any{"type": "boolean"}
	case "double", "float":
		return map[string]any{"type": "number"}
	case "int32", "sint32", "sfixed32", "uint32", "fixed32", "int64", "sint64", "sfixed64", "uint64", "fixed64":
		return map[string]any{"type": "integer"}
	case "bytes":
		// encoding/json reads a []byte field from base64 text.
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	}
	return messageSchema(byName, typ, closed, open)
}
