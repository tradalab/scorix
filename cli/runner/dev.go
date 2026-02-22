package runner

import (
	"context"
	"fmt"
	"os/exec"
)

func Dev(ctx context.Context) error {
	fmt.Println("==> Starting Scorix Dev Mode")

	cmd := exec.CommandContext(ctx, "go", "run", "main.go")
	cmd.Stdout = nil
	cmd.Stderr = nil

	return cmd.Run()
}
