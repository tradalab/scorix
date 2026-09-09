package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tradalab/scorix/internal/cli/template"
)

type InitOptions struct {
	Name    string
	Dir     string
	JSONOut io.Writer // non-nil switches the result to one JSON document on this writer
}

type InitResult struct {
	Name string `json:"name,omitempty"`
	Dir  string `json:"dir,omitempty"`
}

func Init(ctx context.Context, opt InitOptions) error {
	res := &InitResult{}
	err := initProject(ctx, opt, res)
	if opt.JSONOut != nil {
		return EmitJSON(opt.JSONOut, "init", res, err)
	}
	return err
}

func initProject(ctx context.Context, opt InitOptions, res *InitResult) error {
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
	res.Name, res.Dir = opt.Name, root

	fmt.Printf("==> Initializing Scorix project in %s\n", root)

	data := map[string]string{
		"Name":    opt.Name,
		"Package": strings.ToLower(opt.Name),
	}

	if err := writeTemplateFS("static/project", root, data); err != nil {
		return err
	}

	// pinnedAs stays empty when go.mod already existed: this run did not choose the
	// version, so it must not claim one in any message.
	pinnedAs := ""

	fmt.Println("==> Initializing Next.js shell...")
	if err := writeTemplateFS(template.ShellNextJS, filepath.Join(root, "shell"), data); err != nil {
		return err
	}

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

		// Pin the library to the CLI that wrote the scaffold. The old placeholder
		// v0.0.0 was meant for `go mod tidy` to resolve, but tidy cannot resolve a
		// revision that does not exist - so every app scaffolded OUTSIDE this
		// monorepo failed at its first go command, and the sibling `replace` below
		// hid that from everyone who scaffolded inside it.
		pinnedAs = scorixPinVersion(Version().Version)
		e1 := exec.CommandContext(ctx, "go", scorixRequireArgs(pinnedAs)...)
		e1.Dir = root
		e1.Stderr = os.Stderr
		if err := e1.Run(); err != nil {
			return fmt.Errorf("pin scorix: %w", err)
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

	// Each of these three leaves a scaffold that cannot build: no handlers and
	// types, no resolved modules, no node_modules. Reporting them as warnings and
	// then printing "Success!" sent the author looking in the wrong place, and
	// sent an agent reading ok/exit somewhere worse.
	var incomplete []string

	fmt.Println("==> Running initial scorix generate proto...")
	if err := GenerateProto(ctx, GenerateProtoOptions{
		Dir:   root,
		Proto: filepath.Join(root, "idl", "app.proto"),
		Force: true,
	}); err != nil {
		fmt.Printf("warning: initial generate proto failed: %v\n", err)
		incomplete = append(incomplete, "handlers and types are missing: run `scorix generate proto`")
	}

	fmt.Println("==> Running go mod tidy...")
	t := exec.CommandContext(ctx, "go", "mod", "tidy")
	t.Dir = root
	t.Stdout = os.Stdout
	t.Stderr = os.Stderr
	if err := t.Run(); err != nil {
		fmt.Printf("warning: go mod tidy failed: %v\n", err)
		// Name the pin: when it is the pin that cannot be resolved, "run go mod
		// tidy" is advice that loops forever.
		reason := "modules did not resolve: run `go mod tidy`"
		if pinnedAs != "" {
			reason = fmt.Sprintf("go.mod requires github.com/tradalab/scorix@%s and it did not resolve: run `go mod tidy`", pinnedAs)
		}
		incomplete = append(incomplete, reason)
	}

	shellDir := filepath.Join(root, "shell")
	if _, err := os.Stat(filepath.Join(shellDir, "package.json")); err == nil {
		fmt.Println("==> Installing shell dependencies (pnpm install)...")
		pnpm := exec.CommandContext(ctx, "pnpm", "install")
		pnpm.Dir = shellDir
		pnpm.Stdout = os.Stdout
		pnpm.Stderr = os.Stderr
		if err := pnpm.Run(); err != nil {
			fmt.Printf("warning: pnpm install failed: %v\n", err)
			incomplete = append(incomplete, "shell has no node_modules: run `pnpm install` in shell/")
		}
	}

	if len(incomplete) > 0 {
		return &ExitError{Code: ExitFailed, Kind: "incomplete",
			Err: fmt.Errorf("scaffold written to %s but not usable yet - %s", root, strings.Join(incomplete, "; "))}
	}

	fmt.Println("\nSuccess!")

	return nil
}

// scorixRequireArgs picks how the scaffold names its library. A CLI installed at
// a tag knows its own version and can pin without the network; one built from
// source has none, so it asks the proxy. The old code wrote a literal v0.0.0 and
// left `go mod tidy` to resolve it - tidy cannot resolve a revision that does not
// exist, and the sibling replace hid that from everyone inside the monorepo.
func scorixPinVersion(cliVersion string) string {
	if strings.HasPrefix(cliVersion, "v") {
		return cliVersion
	}
	return "latest"
}

func scorixRequireArgs(pin string) []string {
	// `go mod edit` writes without the network but needs a concrete version;
	// "latest" is a query, so it has to go through `go get`.
	if pin != "latest" {
		return []string{"mod", "edit", "-require", "github.com/tradalab/scorix@" + pin}
	}
	return []string{"get", "github.com/tradalab/scorix@latest"}
}
