//go:build windows

package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

// No exec on Windows: a child sharing this console. A .cmd (what npm installs)
// is not something CreateProcess starts by itself.
func runExec(spec ExecSpec, errw io.Writer) int {
	path, err := exec.LookPath(spec.Path)
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return ExitNotFound
	}
	var cmd *exec.Cmd
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cmd", ".bat":
		shell := os.Getenv("ComSpec")
		if shell == "" {
			shell = "cmd.exe"
		}
		cmd = exec.Command(shell)
		// CmdLine replaces the whole line, argv0 included.
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: syscall.EscapeArg(shell) + " " + cmdLine(path, spec.Args)}
	default:
		cmd = exec.Command(path, spec.Args...)
	}
	cmd.Env = execEnv(os.Environ(), spec)
	cmd.Dir = spec.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// Ctrl-C reaches the child through the shared console; this one waits.
	signal.Ignore(os.Interrupt)
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return ExitError
	}
	return ExitOK
}
