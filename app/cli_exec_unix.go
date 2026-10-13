//go:build !windows

package app

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
)

func runExec(spec ExecSpec, errw io.Writer) int {
	path, err := exec.LookPath(spec.Path)
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return ExitNotFound
	}
	if spec.Dir != "" {
		if err := os.Chdir(spec.Dir); err != nil {
			fmt.Fprintln(errw, "error:", err)
			return ExitError
		}
	}
	err = syscall.Exec(path, append([]string{spec.Path}, spec.Args...), execEnv(os.Environ(), spec))
	fmt.Fprintln(errw, "error:", err)
	return ExitError
}
