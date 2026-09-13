package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/tradalab/scorix/internal/cli/runner"
)

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
				"name":  strProp("app name; defaults to the directory name"),
				"dir":   dirProp(),
				"shell": strProp("frontend scaffold: nextjs | vite-react | vanilla-ts (default nextjs)"),
			}),
			run: func(ctx context.Context, a args, out io.Writer) error {
				return runner.Init(ctx, runner.InitOptions{
					JSONOut: out,
					Name:    a.str("name", ""),
					Dir:     a.str("dir", "."),
					Shell:   a.str("shell", ""),
				})
			},
		},
		{
			name: "scorix_surface",
			description: "The IPC surface the proto declares: commands with their arity and middleware, events with their direction and both helper names. " +
				"Reads the proto and writes nothing, so it carries no drift verdict - ask this instead of `generate proto --check` when you only want the contract.",
			schema: obj(map[string]any{
				"dir":   dirProp(),
				"proto": strProp("proto path; defaults to idl/app.proto, or scorix.yaml's proto: key"),
			}),
			run: func(ctx context.Context, a args, out io.Writer) error {
				return runner.Surface(ctx, runner.SurfaceOptions{
					JSONOut: out,
					Dir:     a.str("dir", "."),
					Proto:   a.str("proto", ""),
				})
			},
			// One file parse, no subprocess: it answers while a build holds the turn.
			direct: true,
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
			name: "scorix_dev_start",
			description: "Start the dev loop (frontend dev server plus rebuild-and-relaunch on Go changes) as a background job and return at once. Read its output with scorix_dev_status. " +
				"The app it launches opens a loopback control socket, so scorix_app_eval, scorix_app_dom, scorix_app_input and scorix_app_call can drive the running window.",
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
		{
			name: "scorix_app_status",
			description: "Whether a running app is reachable on its dev control socket, and what it has bound. " +
				"Ask this first: the other scorix_app_* tools need that socket, and the app opens it only under SCORIX_DEV_CONTROL.",
			schema: obj(map[string]any{"dir": dirProp()}),
			run: func(ctx context.Context, a args, out io.Writer) error {
				return runner.AppControl(ctx, runner.AppControlOptions{JSONOut: out, Dir: a.str("dir", "."), Op: "status"})
			},
			direct: true,
		},
		{
			name:        "scorix_app_eval",
			description: "Evaluate JavaScript in the running app's window and return the value. A strict CSP can refuse eval; scorix_app_dom and scorix_app_input do not need it.",
			schema: obj(map[string]any{
				"dir":  dirProp(),
				"code": strProp("the expression to evaluate, in page context"),
			}),
			run: func(ctx context.Context, a args, out io.Writer) error {
				return runner.AppControl(ctx, runner.AppControlOptions{JSONOut: out, Dir: a.str("dir", "."), Op: "eval",
					Args: map[string]any{"code": a.str("code", "")}})
			},
		},
		{
			name:        "scorix_app_dom",
			description: "Match a CSS selector in the running app and return each node's tag, text, attributes and on-screen box. A zero-area box is how an element nobody can click looks.",
			schema: obj(map[string]any{
				"dir":      dirProp(),
				"selector": strProp("CSS selector"),
				"limit":    intProp("how many nodes to return; default 20"),
			}),
			run: func(ctx context.Context, a args, out io.Writer) error {
				return runner.AppControl(ctx, runner.AppControlOptions{JSONOut: out, Dir: a.str("dir", "."), Op: "dom",
					Args: map[string]any{"selector": a.str("selector", ""), "limit": a.num("limit", 20)}})
			},
		},
		{
			name:        "scorix_app_input",
			description: "Click or type into an element of the running app. These are synthesized DOM events, so they drive the page but not a native menu or the OS focus.",
			schema: obj(map[string]any{
				"dir":      dirProp(),
				"selector": strProp("CSS selector of the target element"),
				"action":   strProp("click (default) | type"),
				"text":     strProp("what to type, for action=type"),
				"clear":    boolProp("replace the field's value instead of appending"),
			}),
			run: func(ctx context.Context, a args, out io.Writer) error {
				return runner.AppControl(ctx, runner.AppControlOptions{JSONOut: out, Dir: a.str("dir", "."), Op: "input",
					Args: map[string]any{"selector": a.str("selector", ""), "action": a.str("action", "click"),
						"text": a.str("text", ""), "clear": a.truthy("clear")}})
			},
		},
		{
			name:        "scorix_app_call",
			description: "Invoke a command the running app has bound, by its IPC name, and return the reply. With no name it lists what is bound - which is what the frontend may call.",
			schema: obj(map[string]any{
				"dir":     dirProp(),
				"name":    strProp("IPC command name, e.g. todo:list; omit to list them"),
				"payload": strProp("JSON request body"),
			}),
			run: func(ctx context.Context, a args, out io.Writer) error {
				name := a.str("name", "")
				if name == "" {
					return runner.AppControl(ctx, runner.AppControlOptions{JSONOut: out, Dir: a.str("dir", "."), Op: "commands"})
				}
				req := map[string]any{"name": name}
				if p := a.str("payload", ""); p != "" {
					// Straight into RawMessage, a malformed payload fails inside
					// json.Marshal and reads as an internal error, not as bad input.
					if !json.Valid([]byte(p)) {
						return runner.EmitJSON(out, "app call", nil, fmt.Errorf("payload is not JSON: %s", p))
					}
					req["payload"] = json.RawMessage(p)
				}
				return runner.AppControl(ctx, runner.AppControlOptions{JSONOut: out, Dir: a.str("dir", "."), Op: "call", Args: req})
			},
		},
		{
			name:        "scorix_app_window",
			description: "Drive the running app's native window: show, hide, focus, maximize, restore, resize or retitle, and read back its box and state. A bare call only reports. Web mode has no native window to drive.",
			schema: obj(map[string]any{
				"dir":    dirProp(),
				"action": strProp("info (default) | show | hide | focus | maximize | restore | resize | title"),
				"w":      intProp("width, for action=resize"),
				"h":      intProp("height, for action=resize"),
				"title":  strProp("new title, for action=title"),
			}),
			run: func(ctx context.Context, a args, out io.Writer) error {
				return runner.AppControl(ctx, runner.AppControlOptions{JSONOut: out, Dir: a.str("dir", "."), Op: "window",
					Args: map[string]any{"action": a.str("action", "info"), "w": a.num("w", 0),
						"h": a.num("h", 0), "title": a.str("title", "")}})
			},
		},
		{
			name:        "scorix_app_emit",
			description: "Emit an event to every client of the running app, so a frontend that only listens for events can be driven without binding a command. There is no wait-for-event counterpart yet.",
			schema: obj(map[string]any{
				"dir":  dirProp(),
				"name": strProp("event topic, e.g. todo:changed"),
				"data": strProp("JSON payload"),
			}),
			run: func(ctx context.Context, a args, out io.Writer) error {
				req := map[string]any{"name": a.str("name", "")}
				if d := a.str("data", ""); d != "" {
					if !json.Valid([]byte(d)) {
						return runner.EmitJSON(out, "app emit", nil, fmt.Errorf("data is not JSON: %s", d))
					}
					req["data"] = json.RawMessage(d)
				}
				return runner.AppControl(ctx, runner.AppControlOptions{JSONOut: out, Dir: a.str("dir", "."), Op: "emit", Args: req})
			},
		},
	}
}
