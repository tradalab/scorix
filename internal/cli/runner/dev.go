package runner

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
}

const defaultDevURL = "http://localhost:3000"

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
		fmt.Println("==> Starting shell dev server (pnpm dev)...")
		devCmd := exec.CommandContext(ctx, "pnpm", "dev")
		devCmd.Dir = shellDir
		devCmd.Stdout = os.Stdout
		devCmd.Stderr = os.Stderr
		if err := devCmd.Start(); err != nil {
			return fmt.Errorf("start shell dev server: %w", err)
		}
		defer func() { _ = devCmd.Process.Kill() }()

		devURL = defaultDevURL
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

	args := []string{"run"}
	if cfg, _ := loadProjectConfig(cfgPath); cfg != nil && cfg.Build != nil && len(cfg.Build.Tags) > 0 {
		args = append(args, "-tags", strings.Join(cfg.Build.Tags, ","))
	}
	args = append(args, ".", "-mode", "app")

	fmt.Println("==> Starting Scorix in dev mode (app)...")
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if devURL != "" {
		cmd.Env = append(cmd.Env, "SCORIX_DEV_URL="+devURL)
	}
	return cmd.Run()
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
