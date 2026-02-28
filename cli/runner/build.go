package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

type BuildOptions struct {
	Target  string
	Release bool
}

func Build(ctx context.Context, opt BuildOptions) error {
	fmt.Println("==> Building Scorix app")
	fmt.Println("Target:", opt.Target)

	cmd := exec.CommandContext(ctx, "go", "build", "-o", "dist/app")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
