package runner

import (
	"context"
	"fmt"
	"os"
)

func Create(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing project name")
	}
	name := args[0]

	fmt.Println("==> Creating Scorix project:", name)
	return os.MkdirAll(name, 0755)
}
