package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/tradalab/scorix/config"
	"github.com/tradalab/scorix/logger"
)

// Long enough for a cold start, short enough not to hang a client forever.
var mcpLaunchWait = 20 * time.Second

// errMCPRefused: an app that is running and said no, which a restart cannot change.
var errMCPRefused = errors.New("the running app refused the relay")

// RunMCPProxy is the whole process for --mcp: it relays stdio to the running
// instance, starting the app first. Call it before New, which logs to stdout.
func RunMCPProxy(manifest []byte) error {
	logger.New(logger.Config{Level: "warn", Format: "console", Output: "stderr"})
	cfg, _, err := resolveConfig(Options{Manifest: manifest})
	if err != nil {
		return err
	}
	return proxyMCP(context.Background(), cfg, os.Stdin, os.Stdout)
}

func proxyMCP(ctx context.Context, cfg *config.Config, in io.Reader, out io.Writer) error {
	name := cfg.App.Name
	if name == "" {
		name = cfg.App.Identifier
	}
	if !cfg.MCP.Enabled {
		return fmt.Errorf("%s offers no MCP tools: mcp.enabled is off in its scorix.yaml", name)
	}
	if !mcpUserEnabled(name) {
		return fmt.Errorf("MCP is off in %s: turn it on in the app's settings, then restart the MCP client", name)
	}
	conn, err := dialMCP(name)
	if errors.Is(err, errMCPRefused) {
		return err
	}
	if err != nil {
		if err := mcpLaunch(); err != nil {
			return fmt.Errorf("start %s: %w", name, err)
		}
		if conn, err = waitMCP(ctx, name, mcpLaunchWait); err != nil {
			return err
		}
	}
	defer conn.Close()
	go func() {
		_, _ = io.Copy(conn, in)
		// The client is done: half-close, so the app still answers what it was asked.
		if hc, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = hc.CloseWrite()
			return
		}
		_ = conn.Close()
	}()
	_, err = io.Copy(out, conn)
	return err
}

func dialMCP(name string) (net.Conn, error) {
	b, err := os.ReadFile(filepath.Join(mcpDirOf(name), mcpEndpointFile))
	if err != nil {
		return nil, err
	}
	var ep mcpEndpoint
	if err := json.Unmarshal(b, &ep); err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("unix", ep.Addr, time.Second)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(mcpHelloWait))
	hello, _ := json.Marshal(map[string]string{"token": ep.Token})
	if _, err := conn.Write(append(hello, '\n')); err != nil {
		_ = conn.Close()
		return nil, err
	}
	ack, err := readLine(conn)
	var res struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err == nil {
		err = json.Unmarshal(ack, &res)
	}
	if err == nil && !res.OK {
		err = fmt.Errorf("%w: %s", errMCPRefused, res.Error)
	}
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// One byte at a time: a buffered reader would swallow the start of the MCP stream.
func readLine(r io.Reader) ([]byte, error) {
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := r.Read(b)
		if n == 1 {
			if b[0] == '\n' {
				return line, nil
			}
			if line = append(line, b[0]); len(line) > 4096 {
				return nil, errors.New("handshake line too long")
			}
		}
		if err != nil {
			return nil, err
		}
	}
}

func waitMCP(ctx context.Context, name string, limit time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(limit)
	for {
		conn, err := dialMCP(name)
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s was started but its MCP socket did not open within %s: %w", name, limit, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Test seam. No stdio in production: the client's pipes stay the client's.
var mcpLaunch = func() error {
	exe, err := mcpSelfCommand()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe)
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
