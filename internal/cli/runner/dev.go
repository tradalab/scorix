package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type DevOptions struct {
	Dir string
}

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

	fmt.Println("==> Building shell...")
	shellDir := filepath.Join(root, "shell")
	if _, err := os.Stat(filepath.Join(shellDir, "package.json")); err == nil {
		buildCmd := exec.CommandContext(ctx, "pnpm", "build")
		buildCmd.Dir = shellDir
		buildCmd.Stdout = os.Stdout
		buildCmd.Stderr = os.Stderr
		if err := buildCmd.Run(); err != nil {
			return fmt.Errorf("shell build failed: %w", err)
		}
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
	return cmd.Run()
}
