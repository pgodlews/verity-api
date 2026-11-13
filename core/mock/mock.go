// Package mock serves a fake HTTP API from an OpenAPI 3 document, a WSDL 1.1
// document, or a Verity project's mocks section. The CLI and, later, the
// desktop app construct the same server; tests use Handler or httptest.
package mock

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/verr"
)

// Kind is the detected input of a mock server.
type Kind string

const (
	KindOpenAPI Kind = "openapi"
	KindWSDL    Kind = "wsdl"
	KindProject Kind = "project"
)

// Options configures a mock server. Host defaults to 127.0.0.1. Port 0 asks
// the OS for a free port. Delay is added before every response unless a
// project mock sets its own delayMs. Seed fixes generated sample values.
// Source is the spec or project path; OpenAPI externalValue examples resolve
// relative to its directory.
type Options struct {
	Host     string
	Port     int
	Validate bool
	Delay    time.Duration
	Seed     int64
	Source   string
	// AllowExternalFiles lets a project's include read fragments outside
	// the project directory (--allow-external-files).
	AllowExternalFiles bool
}

// Server is a bound mock listener. Run serves until ctx is cancelled, then
// shuts down. URL is absolute and includes the port the OS selected.
type Server struct {
	URL  string
	Port int
	Kind Kind

	http *http.Server
	ln   net.Listener
}

// Handler returns the mock HTTP handler for a spec or project file.
func Handler(path string, opts Options) (http.Handler, error) {
	h, _, _, err := build(path, nil, true, opts)
	return h, err
}

// HandlerBytes returns the mock HTTP handler for an OpenAPI, WSDL, or project
// document. Project includes are not loaded; use Handler for those.
func HandlerBytes(data []byte, opts Options) (http.Handler, error) {
	h, _, _, err := build("", data, false, opts)
	return h, err
}

// Open binds opts.Host:opts.Port and returns a server that is not yet
// serving. Call Run to accept connections.
func Open(path string, opts Options) (*Server, error) {
	if opts.Port < 0 {
		return nil, verr.New(verr.CodeConfig, "port must be >= 0")
	}
	if opts.Delay < 0 {
		return nil, verr.New(verr.CodeConfig, "delay must be >= 0")
	}
	h, kind, wait, err := build(path, nil, true, opts)
	if err != nil {
		return nil, err
	}
	host := opts.Host
	if host == "" {
		host = "127.0.0.1"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(opts.Port)))
	if err != nil {
		return nil, verr.Wrap(verr.CodeConfig, err, "listen")
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		ln.Close()
		return nil, verr.New(verr.CodeInternal, "listener address is not TCP")
	}
	dial := host
	if advertised, all := dialHost(host); all {
		dial = advertised
		fmt.Fprintf(os.Stderr, "mock: listening on all interfaces (%s); the printed URL is loopback\n", host)
	}
	readTO, writeTO, idleTO := listenTimeouts(wait)
	return &Server{
		URL:  "http://" + net.JoinHostPort(dial, strconv.Itoa(tcp.Port)),
		Port: tcp.Port,
		Kind: kind,
		ln:   ln,
		http: &http.Server{
			Handler:           h,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       readTO,
			WriteTimeout:      writeTO,
			IdleTimeout:       idleTO,
		},
	}, nil
}

// Run accepts connections until ctx is cancelled, then shuts down.
func (s *Server) Run(ctx context.Context) error {
	errc := make(chan error, 1)
	go func() { errc <- s.http.Serve(s.ln) }()
	select {
	case <-ctx.Done():
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := s.http.Shutdown(cctx)
		<-errc
		if errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func build(path string, data []byte, fromFile bool, opts Options) (http.Handler, Kind, time.Duration, error) {
	if opts.Delay < 0 {
		return nil, "", 0, verr.New(verr.CodeConfig, "delay must be >= 0")
	}
	var err error
	if fromFile {
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, "", 0, verr.Wrap(verr.CodeConfig, err, "read mock input")
		}
		opts.Source = path
	}
	kind, err := Detect(data)
	if err != nil {
		return nil, "", 0, err
	}
	wait := opts.Delay
	var h http.Handler
	switch kind {
	case KindOpenAPI:
		h, err = newOpenAPI(data, opts)
	case KindWSDL:
		h, err = newWSDL(data, opts)
	case KindProject:
		var p *model.Project
		if fromFile {
			p, err = model.LoadWith(path, model.LoadOptions{AllowExternalFiles: opts.AllowExternalFiles})
			if err != nil {
				return nil, "", 0, verr.Wrap(verr.CodeConfig, err, "load project")
			}
		} else {
			p, err = unmarshalProject(data)
			if err != nil {
				return nil, "", 0, err
			}
		}
		if verrs := p.ValidateForMock(); len(verrs) > 0 {
			return nil, "", 0, verrs[0]
		}
		wait = responseWait(opts, p.Mocks)
		h = newProjectHandler(p, opts)
	default:
		return nil, "", 0, verr.Newf(verr.CodeConfig, "unsupported mock input %q", kind)
	}
	if err != nil {
		return nil, "", 0, err
	}
	return guard(h), kind, wait, nil
}

// dialHost is the address printed for clients. A wildcard bind stays on all
// interfaces, but the URL a user copies is loopback.
func dialHost(bind string) (string, bool) {
	bare := strings.Trim(strings.TrimSpace(bind), "[]")
	switch bare {
	case "0.0.0.0":
		return "127.0.0.1", true
	case "::":
		return "::1", true
	default:
		return bind, false
	}
}

// listenTimeouts covers a slow body and a response delayed by --delay or
// delayMs. Read and write limits are the longest response wait plus 30s.
func listenTimeouts(delay time.Duration) (read, write, idle time.Duration) {
	if delay < 0 {
		delay = 0
	}
	const slack = 30 * time.Second
	return delay + slack, delay + slack, 60 * time.Second
}

func responseWait(opts Options, mocks []model.Mock) time.Duration {
	d := opts.Delay
	for _, m := range mocks {
		if md := time.Duration(m.DelayMs) * time.Millisecond; md > d {
			d = md
		}
	}
	return d
}

// guard turns an ordinary handler panic into a 500 problem document. A stack
// overflow is not a recoverable panic; callers still have to bound recursion.
func guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				writeProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal error", nil)
			}
		}()
		h.ServeHTTP(w, r)
	})
}

func pause(r *http.Request, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-r.Context().Done():
	}
}
