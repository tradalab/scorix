package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tradalab/scorix/internal/cli/template"
)

type InitOptions struct {
	Name string
	Dir  string
}

func Init(ctx context.Context, opt InitOptions) error {
	if opt.Name == "" {
		cwd, _ := os.Getwd()
		opt.Name = filepath.Base(cwd)
	}
	if opt.Dir == "" {
		opt.Dir = "."
	}

	root, err := filepath.Abs(opt.Dir)
	if err != nil {
		return err
	}

	fmt.Printf("==> Initializing Scorix project in %s\n", root)

	data := map[string]string{
		"Name":    opt.Name,
		"Package": strings.ToLower(opt.Name),
	}

	// 1. Initializing core project files (scorix.yaml, proto, etc.)
	if err := writeTemplateFS("static/project", root, data); err != nil {
		return err
	}

	// 2. Initializing shell (Next.js)
	fmt.Println("==> Initializing Next.js shell...")
	if err := writeTemplateFS(template.ShellNextJS, filepath.Join(root, "shell"), data); err != nil {
		return err
	}

	// 3. Setup Go module
	if _, err := os.Stat(filepath.Join(root, "go.mod")); os.IsNotExist(err) {
		fmt.Println("==> Running go mod init...")
		cmd := exec.CommandContext(ctx, "go", "mod", "init", opt.Name)
		cmd.Dir = root
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("go mod init: %w", err)
		}

		fmt.Println("==> Adding scorix dependency...")

		e1 := exec.CommandContext(ctx, "go", "mod", "edit", "-require", "github.com/tradalab/scorix@v0.0.0")
		e1.Dir = root
		e1.Stderr = os.Stderr
		if err := e1.Run(); err != nil {
			return fmt.Errorf("go mod edit -require scorix: %w", err)
		}

		// Only add a replace directive if a sibling scorix checkout exists (monorepo layout).
		scorixCheckout := filepath.Join(filepath.Dir(root), "scorix")
		if _, err := os.Stat(filepath.Join(scorixCheckout, "go.mod")); err == nil {
			rel, err := filepath.Rel(root, scorixCheckout)
			if err != nil || rel == "" {
				rel = "../scorix"
			}
			rel = filepath.ToSlash(rel)
			fmt.Printf("==> Detected monorepo layout — replacing scorix with %s\n", rel)
			e2 := exec.CommandContext(ctx, "go", "mod", "edit", "-replace", "github.com/tradalab/scorix="+rel)
			e2.Dir = root
			e2.Stderr = os.Stderr
			if err := e2.Run(); err != nil {
				return fmt.Errorf("go mod edit -replace scorix: %w", err)
			}
		}
	}

	// 4. Run generate proto for the first time
	fmt.Println("==> Running initial scorix generate proto...")
	if err := GenerateProto(ctx, GenerateProtoOptions{
		Dir:   root,
		Proto: filepath.Join(root, "proto", "app.proto"),
		Force: true,
	}); err != nil {
		fmt.Printf("warning: initial generate proto failed: %v\n", err)
	}

	// 5. Run go mod tidy again after generation
	fmt.Println("==> Running go mod tidy...")
	t := exec.CommandContext(ctx, "go", "mod", "tidy")
	t.Dir = root
	t.Stdout = os.Stdout
	t.Stderr = os.Stderr
	t.Run()

	// 6. Run pnpm install in shell
	shellDir := filepath.Join(root, "shell")
	if _, err := os.Stat(filepath.Join(shellDir, "package.json")); err == nil {
		fmt.Println("==> Installing shell dependencies (pnpm install)...")
		pnpm := exec.CommandContext(ctx, "pnpm", "install")
		pnpm.Dir = shellDir
		pnpm.Stdout = os.Stdout
		pnpm.Stderr = os.Stderr
		if err := pnpm.Run(); err != nil {
			fmt.Printf("warning: pnpm install failed: %v\n", err)
			fmt.Println("Please run 'pnpm install' manually in the shell directory.")
		}
	}

	fmt.Println("\nSuccess!")

	return nil
}
