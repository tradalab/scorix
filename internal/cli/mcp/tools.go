package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/tradalab/scorix/internal/cli/runner"
)

type tool struct {
	name        string
	description string
	schema      map[string]any
	run         func(ctx context.Context, a args, out io.Writer) error
	timeout     time.Duration // zero uses defaultToolTimeout
	direct      bool          // answers on the read loop, takes no turn
}

// invoke returns the tool's JSON document and whether it failed. The document is
// the runner's own envelope, so an agent branches on the same ok/exit/kind it
// gets from the CLI - this layer is transport, not translation.
func (t tool) invoke(ctx context.Context, raw map[string]any) (text string, failed bool) {
	var buf bytes.Buffer
	err := t.guarded(ctx, args(raw), &buf)
	if buf.Len() == 0 {
		return errorEnvelope(t.name, err), true
	}
	return buf.String(), err != nil
}

// A panic in a runner would take the whole server down with it, and the client
// would see the pipe close rather than which tool broke.
func (t tool) guarded(ctx context.Context, a args, out io.Writer) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return t.run(ctx, a, out)
}

// Reuses the runner's writer so the fallback cannot drift into a second envelope
// shape; a tool only lands here when it panicked before writing its own.
func errorEnvelope(command string, err error) string {
	if err == nil {
		err = errors.New("produced no result")
	}
	var b bytes.Buffer
	_ = runner.EmitJSON(&b, command, nil, err)
	return b.String()
}

type args map[string]any

func (a args) str(key, def string) string {
	if s, ok := a[key].(string); ok && s != "" {
		return s
	}
	return def
}

func (a args) truthy(key string) bool {
	b, _ := a[key].(bool)
	return b
}

// truthyOr keeps a flag whose CLI default is true (dev --watch) at true when the
// caller says nothing, instead of flipping it to Go's zero value.
func (a args) truthyOr(key string, def bool) bool {
	if b, ok := a[key].(bool); ok {
		return b
	}
	return def
}

func (a args) num(key string, def int) int {
	if f, ok := a[key].(float64); ok && f > 0 {
		return int(f)
	}
	return def
}

func (a args) strs(key string) []string {
	raw, ok := a[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func obj(props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props}
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func listProp(desc string) map[string]any {
	return map[string]any{
		"type":        "array",
		"items":       map[string]any{"type": "string"},
		"description": desc,
	}
}

func dirProp() map[string]any {
	return strProp("project root; defaults to the working directory")
}

func builtinTools(s *Server) []tool {
	return []tool{
		{
			name:        "scorix_doctor",
			description: "Check the toolchain a scorix project needs (Go version, node/pnpm, platform SDKs). Run this first when a build fails for no obvious reason.",
			schema:      obj(map[string]any{}),
			run: func(ctx context.Context, _ args, out io.Writer) error {
				return runner.Doctor(ctx, runner.DoctorOptions{JSONOut: out})
			},
		},
		{
			name: "scorix_generate",
			description: "Regenerate code from the declared surface: kind=proto rebuilds IPC handlers plus Go and TS types from the proto file; kind=model rebuilds sqlx CRUD from the SQL schema. " +
				"Set check=true to report drift instead of writing, which is how CI proves the checked-in files still match their source.",
			schema: obj(map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"proto", "model"},
					"description": "which generator to run",
				},
				"dir":     dirProp(),
				"file":    strProp("source file; defaults to the project's proto or schema path"),
				"dialect": strProp("model only: sqlite, mysql or postgres"),
				"check":   boolProp("report drift instead of writing"),
				"force":   boolProp("overwrite files that would normally be kept"),
			}),
			run: func(ctx context.Context, a args, out io.Writer) error {
				switch kind := a.str("kind", "proto"); kind {
				case "proto":
					return runner.GenerateProto(ctx, runner.GenerateProtoOptions{
						JSONOut: out,
						Proto:   a.str("file", ""),
						Dir:     a.str("dir", "."),
						Force:   a.truthy("force"),
						Check:   a.truthy("check"),
					})
				case "model":
					return runner.GenerateModel(ctx, runner.GenerateModelOptions{
						JSONOut: out,
						Schema:  a.str("file", ""),
						Dir:     a.str("dir", "."),
						Dialect: a.str("dialect", ""),
						Force:   a.truthy("force"),
						Check:   a.truthy("check"),
					})
				default:
					return fmt.Errorf("kind must be proto or model, got %q", kind)
				}
			},
		},
		{
			name: "scorix_validate",
			description: "Check scorix.yaml, the proto and the SQL schema before generating from them. Findings carry severity, source and path: " +
				"error means generate cannot run, warn means it will run and produce something the author probably did not intend. Nothing is written.",
			schema: obj(map[string]any{"dir": dirProp()}),
			run: func(ctx context.Context, a args, out io.Writer) error {
				return runner.Validate(ctx, runner.ValidateOptions{JSONOut: out, Dir: a.str("dir", ".")})
			},
		},
		{
			name:        "scorix_build",
			timeout:     30 * time.Minute,
			description: "Build the app binary. A failure carries diagnostics[] with file, line and message, so there is no compiler output to parse.",
			schema: obj(map[string]any{
				"dir":           dirProp(),
				"os":            strProp("target GOOS; defaults to the host"),
				"arch":          strProp("target GOARCH; defaults to the host"),
				"output":        strProp("output path"),
				"tags":          listProp("extra build tags"),
				"skip_frontend": boolProp("reuse the shell already built into the project"),
			}),
			run: func(ctx context.Context, a args, out io.Writer) error {
				return runner.Build(ctx, runner.BuildOptions{
					JSONOut:      out,
					Dir:          a.str("dir", "."),
					OS:           a.str("os", ""),
					Arch:         a.str("arch", ""),
					Output:       a.str("output", ""),
					Tags:         a.strs("tags"),
					SkipFrontend: a.truthy("skip_frontend"),
				})
			},
		},
		{
			name:        "scorix_test",
			description: "Run the app's Go tests. A failing test comes back in failures[] with its package, name and output; a compile error comes back in diagnostics[] with file and line, so there is no terminal output to scrape.",
			schema: obj(map[string]any{
				"dir":  dirProp(),
				"run":  strProp("only run tests matching this pattern"),
				"race": boolProp("enable the race detector"),
				"tags": listProp("extra build tags"),
			}),
			timeout: 30 * time.Minute,
			run: func(ctx context.Context, a args, out io.Writer) error {
				return runner.Test(ctx, runner.TestOptions{
					JSONOut: out, Dir: a.str("dir", "."), Run: a.str("run", ""),
					Race: a.truthy("race"), Tags: a.strs("tags"),
				})
			},
		},
		{
			name:        "scorix_lint",
			description: "Run go vet over the app. Findings come back with file, line and message rather than as text.",
			schema:      obj(map[string]any{"dir": dirProp(), "tags": listProp("extra build tags")}),
			timeout:     30 * time.Minute,
			run: func(ctx context.Context, a args, out io.Writer) error {
				return runner.Lint(ctx, runner.LintOptions{JSONOut: out, Dir: a.str("dir", "."), Tags: a.strs("tags")})
			},
		},
		{
			name:        "scorix_package",
			timeout:     30 * time.Minute,
			description: "Build the app and wrap it in its platform installer. Native installers need their own OS toolchain, so this targets the host unless told otherwise.",
			schema: obj(map[string]any{
				"dir":           dirProp(),
				"os":            strProp("target GOOS; defaults to the host"),
				"arch":          strProp("target GOARCH; defaults to the host"),
				"format":        strProp("installer format; defaults to what the manifest declares"),
				"tags":          listProp("extra build tags"),
				"skip_frontend": boolProp("reuse the shell already built into the project"),
				"skip_sign":     boolProp("skip code signing"),
			}),
			run: func(ctx context.Context, a args, out io.Writer) error {
				return runner.Package(ctx, runner.PackageOptions{
					JSONOut:      out,
					Dir:          a.str("dir", "."),
					OS:           a.str("os", ""),
					Arch:         a.str("arch", ""),
					Format:       a.str("format", ""),
					Tags:         a.strs("tags"),
					SkipFrontend: a.truthy("skip_frontend"),
					SkipSign:     a.truthy("skip_sign"),
				})
			},
		},
		{
			name:        "scorix_appcast",
			timeout:     30 * time.Minute,
			description: "Build the signed update manifest and checksum file from a directory of installers. One run covers every platform it finds; running it once per OS leaves each manifest describing a third of the release.",
			schema: obj(map[string]any{
				"dir":           dirProp(),
				"artifacts_dir": strProp("directory holding the built installers"),
				"base_urls":     listProp("every host that will serve the artifacts"),
			}),
			run: func(ctx context.Context, a args, out io.Writer) error {
				return runner.Appcast(ctx, runner.AppcastOptions{
					JSONOut:      out,
					Dir:          a.str("dir", "."),
					ArtifactsDir: a.str("artifacts_dir", ""),
					BaseURLs:     a.strs("base_urls"),
				})
			},
		},
		{
			name:        "scorix_init",
			timeout:     30 * time.Minute,
			description: "Scaffold a new scorix project: manifest, proto surface, SQL schema, Go entrypoint and the frontend shell.",
			schema: obj(map[string]any{
				"name": strProp("app name; defaults to the directory name"),
				"dir":  dirProp(),
			}),
			run: func(ctx context.Context, a args, out io.Writer) error {
				return runner.Init(ctx, runner.InitOptions{
					JSONOut: out,
					Name:    a.str("name", ""),
					Dir:     a.str("dir", "."),
				})
			},
		},
		{
			name: "scorix_status",
			description: "What this server is running right now, and for how long. It answers even while a tool is stuck, " +
				"which is when it matters: every other tool queues behind the one ahead of it.",
			schema: obj(map[string]any{}),
			direct: true,
			run: func(_ context.Context, _ args, out io.Writer) error {
				return runner.EmitJSON(out, "status", map[string]any{"running": s.running()}, nil)
			},
		},
		{
			name:        "scorix_dev_start",
			description: "Start the dev loop (frontend dev server plus rebuild-and-relaunch on Go changes) as a background job and return at once. Read its output with scorix_dev_status.",
			schema: obj(map[string]any{
				"dir":    dirProp(),
				"url":    strProp("use an already-running frontend dev server instead of spawning one"),
				"legacy": boolProp("build the shell once instead of running the dev server, so no hot reload"),
				"watch":  boolProp("rebuild and relaunch the Go app on source changes; defaults to true"),
			}),
			run:     func(ctx context.Context, a args, out io.Writer) error { return s.dev.start(ctx, a, out) },
			timeout: 2 * time.Minute,
		},
		{
			name:        "scorix_dev_stop",
			description: "Stop the dev job started by scorix_dev_start.",
			schema:      obj(map[string]any{}),
			run:         func(ctx context.Context, _ args, out io.Writer) error { return s.dev.stop(ctx, out) },
		},
		{
			name:        "scorix_dev_status",
			description: "Report whether the dev job is running, and return the tail of its output where compile errors and reload failures appear.",
			schema: obj(map[string]any{
				"lines": intProp("how many trailing output lines to return; default 40"),
			}),
			run:    func(_ context.Context, a args, out io.Writer) error { return s.dev.status(a, out) },
			direct: true,
		},
	}
}

// checkArgs rejects an argument whose JSON type contradicts the schema the tool
// published. Ignoring it instead is worse than an error: a dir that is not a
// string falls back to "." and the command runs against the wrong directory.
func checkArgs(schema map[string]any, given map[string]any) error {
	props, _ := schema["properties"].(map[string]any)
	for key, val := range given {
		spec, ok := props[key].(map[string]any)
		if !ok || val == nil {
			continue // unknown keys and explicit nulls are the caller's business
		}
		want, _ := spec["type"].(string)
		if !matchesJSONType(want, val) {
			return fmt.Errorf("argument %q must be %s, got %T", key, want, val)
		}
	}
	return nil
}

func matchesJSONType(want string, val any) bool {
	switch want {
	case "string":
		_, ok := val.(string)
		return ok
	case "boolean":
		_, ok := val.(bool)
		return ok
	case "integer", "number":
		_, ok := val.(float64)
		return ok
	case "array":
		_, ok := val.([]any)
		return ok
	case "object":
		_, ok := val.(map[string]any)
		return ok
	}
	return true // no declared type: nothing to contradict
}

// Every tool answers with the runner's envelope, so one schema describes them
// all; a per-tool `data` shape would be a second copy of what the runner types
// already say.
func envelopeSchema() map[string]any {
	return obj(map[string]any{
		"command": strProp("the command that ran"),
		"ok":      boolProp("false when it failed"),
		"exit":    intProp("0 ok - 1 ran and failed - 2 called wrong - 3 drift found - 4 a required tool is missing"),
		"kind":    strProp("failure class, present only on failure"),
		"error":   strProp("what went wrong, present only on failure"),
		"data":    map[string]any{"type": "object", "description": "per-command result"},
	})
}
