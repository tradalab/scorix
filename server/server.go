// Package server hosts a scorix service on the network: one listener that speaks HTTP/1.1
// and cleartext HTTP/2, so gRPC clients without TLS reach it as well as browsers do.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	"github.com/tradalab/scorix/observe"
)

type Options struct {
	Addr string
	// Log gets one access line per request and rides in each request's context; nil logs
	// nothing. A line names the method, path, status, duration and size, never the query or body.
	Log *slog.Logger
	// Grace is how long Run waits for calls in flight once ctx ends; zero means 10s.
	Grace time.Duration
	// MaxInFlight caps the calls served at once across every Mount; the next one gets 503,
	// which Connect and gRPC clients read as unavailable. Zero means no cap; healthz is never shed.
	MaxInFlight int
	// TLS serves HTTP/1.1 and HTTP/2 over TLS with these certificates instead of cleartext
	// HTTP/2; nil serves cleartext, for a mesh or ingress that terminates TLS in front.
	TLS *tls.Config
	// OnShed hears of each call MaxInFlight or ShedAboveCPU turned away, for a metric; nil hears nothing.
	OnShed func(r *http.Request)
	// ShedAboveCPU, a share such as 0.9, sheds with 503 while CPU use of the cgroup quota or GOMAXPROCS is
	// above it and more calls are in flight than recently finished. gRPC calls and Connect streams are judged
	// but never counted. Zero never sheds, above 1 fails Run, and an unreadable CPU sheds nothing.
	ShedAboveCPU float64
}

type Server struct {
	opt   Options
	mux   *http.ServeMux
	slots chan struct{}
	load  *shedder
	cpu   *sampler
	cpuOn atomic.Bool
	root  fs.FS
}

func New(opt Options) *Server {
	if opt.Grace == 0 {
		opt.Grace = 10 * time.Second
	}
	s := &Server{opt: opt, mux: http.NewServeMux(), root: os.DirFS("/")}
	if opt.MaxInFlight > 0 {
		s.slots = make(chan struct{}, opt.MaxInFlight)
	}
	if opt.ShedAboveCPU > 0 {
		s.cpu = &sampler{}
		s.load = &shedder{threshold: opt.ShedAboveCPU, cpu: s.cpu.load, now: time.Now}
	}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return s
}

// Mount serves h for every path under prefix, for example "/shorten.Rpc/". Each outer wraps it
// outside shedding, the first outermost, so a CORS outer still answers a preflight and labels a shed 503.
func (s *Server) Mount(prefix string, h http.Handler, outer ...func(http.Handler) http.Handler) {
	h = s.shed(h)
	for i := len(outer) - 1; i >= 0; i-- {
		h = outer[i](h)
	}
	s.mux.Handle(prefix, h)
}

// Probe serves h at pattern, such as "GET /api/v1/healthz", and never sheds it, so an orchestrator
// polling it under load does not restart a busy pod. Nothing caps h, so it must stay cheap.
func (s *Server) Probe(pattern string, h http.Handler) { s.mux.Handle(pattern, h) }

func (s *Server) refuse(w http.ResponseWriter, r *http.Request, why string) {
	if s.opt.OnShed != nil {
		s.opt.OnShed(r)
	}
	w.Header().Set("Retry-After", "1")
	http.Error(w, why, http.StatusServiceUnavailable)
}

func (s *Server) shed(h http.Handler) http.Handler {
	if s.slots == nil && s.load == nil {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.load != nil && s.cpuOn.Load() {
			if streams(r) {
				if !s.load.admit(s.load.now()) {
					s.refuse(w, r, "the server is out of CPU")
					return
				}
			} else {
				done, ok := s.load.allow()
				if !ok {
					s.refuse(w, r, "the server is out of CPU")
					return
				}
				defer done()
			}
		}
		if s.slots == nil {
			h.ServeHTTP(w, r)
			return
		}
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
			h.ServeHTTP(w, r)
		default:
			s.refuse(w, r, "the server is at its in-flight cap")
		}
	})
}

func streams(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	return strings.HasPrefix(ct, "application/grpc") || strings.HasPrefix(ct, "application/connect+")
}

func (s *Server) watchCPU(ctx context.Context) error {
	if s.load == nil {
		return nil
	}
	if s.opt.ShedAboveCPU > 1 {
		return fmt.Errorf("server: ShedAboveCPU %v is not a share between 0 and 1", s.opt.ShedAboveCPU)
	}
	src, err := cpuOf(s.root, runtime.GOMAXPROCS(0))
	if err == nil {
		s.cpu.src = src
		err = s.cpu.sample(time.Now())
	}
	if err != nil {
		if s.opt.Log != nil {
			s.opt.Log.Warn("server: CPU use cannot be read, so ShedAboveCPU sheds nothing", "error", err.Error())
		}
		return nil
	}
	s.cpuOn.Store(true)
	go func() {
		tick := time.NewTicker(sampleEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tick.C:
				s.cpu.sample(now)
			}
		}
	}()
	return nil
}

// Run serves on ln until ctx ends, then stops taking calls and waits up to Grace for the
// ones in flight. It returns nil after a clean stop, or an error once Grace ran out and the
// calls still running, streams among them, had their contexts cancelled and connections closed.
func (s *Server) Run(ctx context.Context, ln net.Listener) error {
	if err := s.watchCPU(ctx); err != nil {
		return err
	}
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	if s.opt.TLS != nil {
		protocols.SetHTTP2(true)
		ln = tls.NewListener(ln, withALPN(s.opt.TLS))
	} else {
		protocols.SetUnencryptedHTTP2(true)
	}
	srv := &http.Server{
		Handler:           s.handler(),
		Protocols:         &protocols,
		ReadHeaderTimeout: 10 * time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	stop, cancel := context.WithTimeout(context.Background(), s.opt.Grace)
	defer cancel()
	err := srv.Shutdown(stop)
	if err != nil {
		srv.Close()
		err = fmt.Errorf("server: calls still running after the %v grace were cut off: %w", s.opt.Grace, err)
	}
	if serveErr := <-done; !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return err
}

func withALPN(c *tls.Config) *tls.Config {
	c = c.Clone()
	if len(c.NextProtos) == 0 {
		c.NextProtos = []string{"h2", "http/1.1"}
	}
	return c
}

type recorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *recorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *recorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func grpcCode(h http.Header) (string, bool) {
	n, err := strconv.Atoi(h.Get(http.TrailerPrefix + "Grpc-Status"))
	if err != nil {
		return "", false
	}
	if n == 0 {
		return "ok", true
	}
	return connect.Code(n).String(), true
}

func (s *Server) handler() http.Handler {
	if s.opt.Log == nil {
		return s.mux
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &recorder{ResponseWriter: w}
		s.mux.ServeHTTP(rec, r.WithContext(observe.WithLogger(r.Context(), s.opt.Log)))
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		attrs := []slog.Attr{
			slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.Int("status", rec.status),
			slog.Duration("duration", time.Since(start)), slog.Int("bytes", rec.bytes),
		}
		if code, ok := grpcCode(rec.Header()); ok {
			attrs = append(attrs, slog.String("code", code))
		}
		s.opt.Log.LogAttrs(r.Context(), slog.LevelInfo, "request", attrs...)
	})
}

// ListenAndRun listens on Options.Addr and calls Run.
func (s *Server) ListenAndRun(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.opt.Addr)
	if err != nil {
		return err
	}
	return s.Run(ctx, ln)
}
