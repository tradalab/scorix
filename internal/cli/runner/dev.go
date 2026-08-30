package runner

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type DevOptions struct {
	Dir string
	// URL of an already-running frontend dev server. Empty → `pnpm dev` is
	// spawned and http://localhost:3000 assumed.
	URL string
	// Legacy disables HMR: build the shell once and serve the embedded assets.
	Legacy bool
	// Watch rebuilds and relaunches the Go app on source changes (proto/schema
	// changes regenerate first). WatchSet marks an explicit flag, which beats
	// scorix.yaml dev.hot_reload.
	Watch    bool
	WatchSet bool
}

const defaultDevPort = 3000

func devServerPort(cfgPath string) int {
	if v := strings.TrimSpace(os.Getenv("SCORIX_DEV_PORT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 65536 {
			return n
		}
		fmt.Fprintf(os.Stderr, "==> ignoring SCORIX_DEV_PORT=%q: not a port\n", v)
	}
	if cfg, _ := loadProjectConfig(cfgPath); cfg != nil && cfg.Dev != nil && cfg.Dev.Port > 0 {
		return cfg.Dev.Port
	}
	return defaultDevPort
}

// Dev starts the shell dev server, waits until it answers, then runs the Go app
// with SCORIX_DEV_URL pointing the window at it for HMR. The dev server dies
// with the app.
func Dev(ctx context.Context, opt DevOptions) error {
	if opt.Dir == "" {
		opt.Dir = "."
	}

	root, err := filepath.Abs(opt.Dir)
	if err != nil {
		return err
	}

	cfgPath := filepath.Join(root, "scorix.yaml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		return fmt.Errorf("scorix.yaml not found in %s", root)
	}

	shellDir := filepath.Join(root, "shell")
	hasShell := false
	if _, err := os.Stat(filepath.Join(shellDir, "package.json")); err == nil {
		hasShell = true
	}

	devURL := opt.URL
	if !opt.Legacy && hasShell && devURL == "" {
		port := devServerPort(cfgPath)
		fmt.Printf("==> Starting shell dev server (pnpm dev) on port %d...\n", port)
		devCmd := exec.CommandContext(ctx, "pnpm", "dev")
		devCmd.Dir = shellDir
		devCmd.Env = append(os.Environ(), "PORT="+strconv.Itoa(port))
		devCmd.Stdout = os.Stdout
		devCmd.Stderr = os.Stderr
		if err := devCmd.Start(); err != nil {
			return fmt.Errorf("start shell dev server: %w", err)
		}
		defer func() { _ = devCmd.Process.Kill() }()

		devURL = fmt.Sprintf("http://localhost:%d", port)
		if err := waitForServer(ctx, devURL, 60*time.Second); err != nil {
			return err
		}
		fmt.Printf("==> Shell dev server ready at %s (HMR active)\n", devURL)
	}

	if opt.Legacy || (!hasShell && devURL == "") {
		if hasShell {
			fmt.Println("==> Building shell (legacy dev — no HMR)...")
			buildCmd := exec.CommandContext(ctx, "pnpm", "build")
			buildCmd.Dir = shellDir
			buildCmd.Stdout = os.Stdout
			buildCmd.Stderr = os.Stderr
			if err := buildCmd.Run(); err != nil {
				return fmt.Errorf("shell build failed: %w", err)
			}
		}
		devURL = ""
	}

	// `go run` below has to compile `//go:embed all:.scorix/dist`, which fails on
	// a fresh project where no frontend has been built yet. The dev window loads
	// from the HMR server, not these assets, so a placeholder is enough.
	if err := ensureEmbedDir(filepath.Join(root, ".scorix", "dist")); err != nil {
		return err
	}

	cfg, _ := loadProjectConfig(cfgPath)
	var tags []string
	if cfg != nil && cfg.Build != nil {
		tags = cfg.Build.Tags
	}
	env := os.Environ()
	if devURL != "" {
		env = append(env, "SCORIX_DEV_URL="+devURL)
	}

	watch := opt.Watch
	if !opt.WatchSet && cfg != nil && cfg.Dev != nil && cfg.Dev.HotReload != nil {
		watch = *cfg.Dev.HotReload
	}
	if watch {
		return devWatch(ctx, root, cfg, tags, env)
	}

	args := []string{"run"}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	args = append(args, ".", "-mode", "app")

	fmt.Println("==> Starting Scorix in dev mode (app)...")
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env
	return cmd.Run()
}

func devWatch(ctx context.Context, root string, cfg *ProjectConfig, tags, env []string) error {
	protoRel, schemaRel := "idl/app.proto", "etc/schema.sql"
	if cfg != nil && cfg.Proto != "" {
		protoRel = cfg.Proto
	}
	if cfg != nil && cfg.Model != nil && cfg.Model.Schema != "" {
		schemaRel = cfg.Model.Schema
	}
	protoRel, schemaRel = filepath.ToSlash(protoRel), filepath.ToSlash(schemaRel)

	if err := os.MkdirAll(filepath.Join(root, ".scorix", "dev"), 0o755); err != nil {
		return err
	}
	ws := newWatchSet(root,
		filepath.Join(root, "scorix.yaml"),
		filepath.Join(root, filepath.FromSlash(protoRel)),
		filepath.Join(root, filepath.FromSlash(schemaRel)))
	ws.scan()

	app := &devApp{ctx: ctx, root: root, env: env, tags: tags, done: make(chan struct{})}
	fmt.Println("==> Building app...")
	if err := app.build(); err != nil { // a broken tree fails here, not after a window opened
		return fmt.Errorf("build failed: %w", err)
	}
	fmt.Println("==> Starting Scorix in dev mode (app, watching Go sources)...")
	if err := app.restart(); err != nil {
		return err
	}
	defer app.stop()

	return devLoop(ctx, ws, protoRel, schemaRel, devHooks{
		regenerate: func(proto, schema bool) error {
			if proto {
				fmt.Println("==> [watch] proto changed: regenerating")
				if err := GenerateProto(ctx, GenerateProtoOptions{Proto: "idl/app.proto", Dir: root}); err != nil {
					return err
				}
			}
			if schema {
				fmt.Println("==> [watch] schema changed: regenerating models")
				if err := GenerateModel(ctx, GenerateModelOptions{Schema: "etc/schema.sql", Dir: root}); err != nil {
					return err
				}
			}
			return nil
		},
		build:   app.build,
		restart: app.restart,
		appDone: app.appDone,
		out:     os.Stdout,
	})
}

// waitForServer polls url until it answers (any HTTP status counts — Next dev
// may 404 the root mid-compile but the socket is what matters).
func waitForServer(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if resp, err := client.Get(url); err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("shell dev server at %s did not become ready within %s", url, timeout)
}
