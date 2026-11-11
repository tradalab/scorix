package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tradalab/scorix/idl"
)

type GenerateOpenAPIOptions struct {
	JSONOut io.Writer
	Proto   string
	Out     string
	Title   string
	Version string
	Check   bool
}

func GenerateOpenAPI(ctx context.Context, opt GenerateOpenAPIOptions) error {
	res := &GenerateResult{Check: opt.Check, Regen: "scorix generate openapi"}
	err := generateOpenAPI(ctx, opt, res)
	if opt.JSONOut != nil {
		return EmitJSON(opt.JSONOut, "generate openapi", res, err)
	}
	return err
}

func generateOpenAPI(ctx context.Context, opt GenerateOpenAPIOptions, res *GenerateResult) error {
	if opt.Proto == "" || opt.Out == "" {
		return fmt.Errorf("generate openapi needs --proto and --out")
	}
	src, err := os.ReadFile(opt.Proto)
	if err != nil {
		return fmt.Errorf("read proto: %w", err)
	}
	pf, err := idl.Parse(filepath.Base(opt.Proto), string(src))
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(openAPI(pf, opt.Title, opt.Version), "", "  ")
	if err != nil {
		return err
	}
	out, err := filepath.Abs(opt.Out)
	if err != nil {
		return err
	}
	return emitGenerated(ctx, filepath.Dir(out), opt.Check, res, []generatedFile{{
		Path:     out,
		Template: "{{ . }}\n",
		Data:     string(body),
		Force:    true,
	}})
}

func openAPI(pf idl.File, title, version string) map[string]any {
	if title == "" {
		title = pf.ProtoPackage
	}
	if version == "" {
		version = "0"
	}
	byName := map[string]idl.Message{}
	for _, m := range pf.Messages {
		byName[m.Name] = m
	}
	used := map[string]bool{}
	var ref func(name string) map[string]any
	ref = func(name string) map[string]any {
		if !used[name] {
			used[name] = true
			for _, f := range byName[name].Fields {
				if _, ok := scalarSchema(f.Type); !ok {
					ref(f.Type)
				}
			}
		}
		return map[string]any{"$ref": "#/components/schemas/" + name}
	}
	typeOf := func(f idl.Field) map[string]any {
		s, ok := scalarSchema(f.Type)
		if !ok {
			s = ref(f.Type)
		}
		if f.Repeated {
			s = map[string]any{"type": "array", "items": s}
		}
		return s
	}
	paths := map[string]map[string]any{}
	anyAuth := false
	for _, svc := range pf.Services {
		for _, rpc := range svc.RPCs {
			h := rpc.HTTP
			if h == nil {
				continue
			}
			req := byName[rpc.RequestType]
			inPath := map[string]bool{}
			for _, p := range h.Params {
				inPath[p] = true
			}
			var params []map[string]any
			for _, f := range req.Fields {
				switch {
				case inPath[f.Name]:
					params = append(params, map[string]any{"name": f.Name, "in": "path", "required": true, "schema": typeOf(f)})
				case f.Header != "":
					params = append(params, map[string]any{"name": f.Header, "in": "header", "schema": typeOf(f)})
				case h.Body == "":
					params = append(params, map[string]any{"name": f.WireName, "in": "query", "schema": typeOf(f)})
				}
			}
			op := map[string]any{
				"operationId": svc.Name + "_" + rpc.Name,
				"tags":        []string{svc.Name},
				"responses": map[string]any{
					"200":     map[string]any{"description": "OK", "content": map[string]any{"application/json": map[string]any{"schema": ref(rpc.ResponseType)}}},
					"default": map[string]any{"description": "The call failed or was refused"},
				},
			}
			if rpc.Doc != "" {
				op["description"] = rpc.Doc
			}
			if len(params) > 0 {
				op["parameters"] = params
			}
			if h.Body == "*" {
				op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": ref(rpc.RequestType)}}}
			}
			if h.Anonymous {
				op["security"] = []any{}
			} else {
				anyAuth = true
			}
			if paths[h.Path] == nil {
				paths[h.Path] = map[string]any{}
			}
			paths[h.Path][strings.ToLower(h.Method)] = op
		}
	}
	schemas := map[string]any{}
	for name := range used {
		props := map[string]any{}
		for _, f := range byName[name].Fields {
			props[f.WireName] = typeOf(f)
		}
		schemas[name] = map[string]any{"type": "object", "properties": props}
	}
	doc := map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]any{"title": title, "version": version},
		"paths":   paths,
		"components": map[string]any{
			"schemas": schemas,
		},
	}
	if anyAuth {
		doc["components"].(map[string]any)["securitySchemes"] = map[string]any{"bearer": map[string]any{"type": "http", "scheme": "bearer"}}
		doc["security"] = []any{map[string]any{"bearer": []any{}}}
	}
	return doc
}
