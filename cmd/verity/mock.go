package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pgodlews/verity-api/core/mock"
)

func cmdMock(args []string) int {
	fs := flag.NewFlagSet("mock", flag.ContinueOnError)
	port := fs.Int("port", 0, "TCP port (0 picks a free port)")
	host := fs.String("host", "127.0.0.1", "bind address; 0.0.0.0 or :: listens on all interfaces (the printed URL stays loopback)")
	validate := fs.Bool("validate", false, "validate OpenAPI requests and return 400 on failure")
	delay := fs.Int("delay", 0, "milliseconds to wait before each response")
	seed := fs.Int64("seed", 0, "seed for generated sample values")
	jsonOut := fs.Bool("json", false, "print the listen URL as JSON")
	fs.SetOutput(os.Stderr)
	pos, err := parsePermuted(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(pos) != 1 {
		fmt.Fprintln(os.Stderr, "mock: usage: verity mock <spec-or-project> [--port 0] [--host 127.0.0.1] [--validate] [--delay MS] [--seed N]")
		return exitUsage
	}
	if strings.TrimSpace(*host) == "" {
		fmt.Fprintln(os.Stderr, "mock: --host must not be empty (127.0.0.1, or 0.0.0.0 to listen on all interfaces)")
		return exitUsage
	}
	if *port < 0 || *delay < 0 {
		fmt.Fprintln(os.Stderr, "mock: --port and --delay must be >= 0")
		return exitUsage
	}
	srv, err := mock.Open(pos[0], mock.Options{
		Host:     *host,
		Port:     *port,
		Validate: *validate,
		Delay:    time.Duration(*delay) * time.Millisecond,
		Seed:     *seed,
	})
	if err != nil {
		return failJSON(*jsonOut, exitConfig, "mock", err)
	}
	if *jsonOut {
		emitJSON(map[string]any{
			"ok":   true,
			"url":  srv.URL,
			"port": srv.Port,
			"kind": string(srv.Kind),
		})
	} else {
		fmt.Printf("mock listening on %s\n", srv.URL)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Run(ctx); err != nil {
		return failJSON(*jsonOut, exitInternal, "mock", err)
	}
	return exitOK
}
