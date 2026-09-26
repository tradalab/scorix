package llamacpp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/tradalab/scorix/llm"
	"github.com/tradalab/scorix/llm/openai"
	"github.com/tradalab/scorix/proc"
)

type ServerOptions struct {
	// Zero picks a free port on 127.0.0.1.
	Port int
	// The vision projector (mmproj) to load beside the model.
	Projector string
	// Serves /v1/embeddings instead of chat. llama-server refuses one without
	// the other, so a model used for both needs two servers.
	Embeddings bool
	// Tokens; zero keeps the model's own. For embeddings it is also the batch:
	// llama-server pools a text over one batch and refuses anything longer.
	Context int
	Args    []string
	// A large model on a slow disk takes minutes; 3 when zero.
	ReadyTimeout time.Duration
}

type Server struct {
	proc *proc.Process
	base string
}

// No -ngl is passed: any value switches off llama-server's own fitting to free
// memory, and a model that does not fit then kills the server instead of
// spilling to the CPU (b9934, 4 GB card).
func Start(ctx context.Context, exe, modelPath string, opt ServerOptions) (*Server, error) {
	// Refused rather than overridden: the port here decides what base URL the
	// health check and every later request use, so a second one in Args means
	// one of the two is wrong whichever way it is resolved.
	if slices.Contains(opt.Args, "--port") {
		return nil, errors.New("llamacpp: pass the port as ServerOptions.Port, not in Args")
	}
	port := opt.Port
	if port == 0 {
		p, err := freePort()
		if err != nil {
			return nil, err
		}
		port = p
	}
	// Built per attempt rather than copied and patched: a copy that rewrites
	// "the element after --port" also rewrites one the caller brought, and in
	// the sibling lemonade package it ate a positional argument.
	build := func(port int) []string {
		args := []string{"-m", modelPath, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--jinja"}
		if opt.Projector != "" {
			args = append(args, "--mmproj", opt.Projector)
		}
		if c := strconv.Itoa(opt.Context); opt.Context > 0 {
			args = append(args, "-c", c)
			if opt.Embeddings {
				args = append(args, "-b", c, "-ub", c)
			}
		}
		if opt.Embeddings {
			args = append(args, "--embeddings")
		}
		return append(args, opt.Args...)
	}
	timeout := opt.ReadyTimeout
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	// A picked port is free when it is picked and taken by the time the child
	// binds it: the listener that proved it free has to be closed first, and
	// anything on the machine can have it in between. Losing that race costs
	// the whole ready timeout and reads as a broken engine, so it is worth one
	// more go with a different number.
	for attempt := 0; ; attempt++ {
		base := fmt.Sprintf("http://127.0.0.1:%d", port)
		p, err := proc.Start(ctx, proc.Spec{
			Path:         exe,
			Args:         build(port),
			Dir:          filepath.Dir(exe),
			Health:       func(ctx context.Context) error { return health(ctx, base) },
			ReadyTimeout: timeout,
			LogLines:     200,
		})
		if err == nil {
			return &Server{proc: p, base: base}, nil
		}
		if attempt > 0 || opt.Port != 0 || !proc.PortTaken(err) {
			return nil, fmt.Errorf("llamacpp: %w", err)
		}
		next, perr := freePort()
		if perr != nil {
			return nil, fmt.Errorf("llamacpp: %w", err)
		}
		port = next
	}
}

// Up to /v1, as the openai driver takes it.
func (s *Server) BaseURL() string { return s.base + "/v1" }

// Local by construction: this process started it on 127.0.0.1.
func (s *Server) Provider(id string, caps ...llm.Capability) (*openai.Provider, error) {
	return openai.New(openai.Config{ID: id, BaseURL: s.BaseURL(), Capabilities: caps})
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
	// 503 while the model is still loading.
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
