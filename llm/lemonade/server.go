package lemonade

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/tradalab/scorix/llm"
	"github.com/tradalab/scorix/proc"
)

type ServerOptions struct {
	// Where lemond keeps the backends it downloads, the models and its config.
	// Required: left to itself it writes into the user's own
	// ~/.cache/lemonade, ~/.config/lemonade and ~/.cache/huggingface.
	Dir string
	// Zero picks a free port on 127.0.0.1.
	Port int
	Args []string
	// One minute when zero. Loading a model happens later, on first use.
	ReadyTimeout time.Duration
}

type Server struct {
	proc *proc.Process
	base string
}

// proc stops the whole tree, so the whisper-server and llama-server lemond
// starts for a model go with it.
func Start(ctx context.Context, exe string, opt ServerOptions) (*Server, error) {
	if opt.Dir == "" {
		return nil, errors.New("lemonade: ServerOptions.Dir is required")
	}
	// Refused rather than overridden: the port here decides what base URL the
	// health check and every later request use, so a second one in Args means
	// one of the two is wrong whichever way it is resolved.
	if slices.Contains(opt.Args, "--port") {
		return nil, errors.New("lemonade: pass the port as ServerOptions.Port, not in Args")
	}
	port := opt.Port
	if port == 0 {
		p, err := freePort()
		if err != nil {
			return nil, err
		}
		port = p
	}
	// Built per attempt rather than copied and patched: the two directories
	// below are positional, so rewriting "the element after --port" in a copy
	// overwrote the cache directory whenever opt.Args ended with --port, and
	// lemond then wrote its backends into a directory named after the port.
	build := func(port int) []string {
		// Broadcast would announce the server to the LAN; an app's private
		// runtime has nobody there to meet.
		args := []string{"--host", "127.0.0.1", "--port", strconv.Itoa(port), "--no-broadcast", "--log-file", "disabled"}
		args = append(args, opt.Args...)
		return append(args, filepath.Join(opt.Dir, "cache"), filepath.Join(opt.Dir, "config"))
	}
	timeout := opt.ReadyTimeout
	if timeout == 0 {
		timeout = time.Minute
	}
	// A picked port is free when it is picked and taken by the time the child
	// binds it: the listener that proved it free has to be closed first. Losing
	// that race costs the whole ready timeout and reads as a broken runtime.
	for attempt := 0; ; attempt++ {
		base := fmt.Sprintf("http://127.0.0.1:%d/api/v1", port)
		p, err := proc.Start(ctx, proc.Spec{
			Path: exe,
			Args: build(port),
			Dir:  filepath.Dir(exe),
			// The cache directory argument does not cover models from Hugging
			// Face: those follow the hub's own variable.
			Env:          append(os.Environ(), "HF_HUB_CACHE="+filepath.Join(opt.Dir, "huggingface")),
			Health:       func(ctx context.Context) error { return health(ctx, base) },
			ReadyTimeout: timeout,
			LogLines:     200,
		})
		if err == nil {
			return &Server{proc: p, base: base}, nil
		}
		if attempt > 0 || opt.Port != 0 || !proc.PortTaken(err) {
			return nil, fmt.Errorf("lemonade: %w", err)
		}
		next, perr := freePort()
		if perr != nil {
			return nil, fmt.Errorf("lemonade: %w", err)
		}
		port = next
	}
}

// Up to /api/v1: the OpenAI API and Lemonade's own share it.
func (s *Server) BaseURL() string { return s.base }

// Local by construction: this process started it on 127.0.0.1.
func (s *Server) Provider(id string, caps ...llm.Capability) (*Provider, error) {
	return New(Config{ID: id, BaseURL: s.base, Capabilities: caps})
}

func (s *Server) Stop(ctx context.Context) error { return s.proc.Stop(ctx) }

func (s *Server) Done() <-chan struct{} { return s.proc.Done() }

func (s *Server) Logs() []string { return s.proc.Logs() }

func health(ctx context.Context, base string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health: HTTP %d", resp.StatusCode)
	}
	return nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
