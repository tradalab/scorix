package runner

import (
	"context"
	"fmt"
	"os/exec"
)

func Doctor(ctx context.Context) error {
	fmt.Println("Checking Go toolchain...")

	_, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("go not found in PATH")
	}

	fmt.Println("OK: Go available")
	return nil
}
