package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/pgodlews/verity-api/core/verr"
)

// Client is the host side of a plugin connection. It launches the plugin
// binary, performs the handshake, and provides RPC. One Client manages one
// subprocess. It is safe for concurrent calls (requests are serialized).
type Client struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	hs      Handshake
	Timeout time.Duration
	stderr  *stderrWriter

	mu     sync.Mutex
	dead   bool
	nextID int
	// name labels the plugin in errors (the manifest name, else the
	// binary's base name). deadReason says why the process was stopped.
	name       string
	deadReason string
}

// markDead stops using the plugin process for the rest of the run. Verity
// does not relaunch plugins: later calls fail with deadError.
func (c *Client) markDead(reason string) {
	if !c.dead {
		c.dead = true
		c.deadReason = reason
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

// deadError is the error every call after markDead returns: plugin_error
// naming the plugin and why it was stopped, with details "plugin" and
// "reason".
func (c *Client) deadError() error {
	reason := c.deadReason
	if reason == "" {
		reason = "it was shut down"
	}
	return verr.Newf(verr.CodePlugin,
		"plugin %q is no longer running: %s. Verity does not restart a plugin during a run; fix the cause and run again", c.name, reason).
		WithDetail("plugin", c.name).WithDetail("reason", reason)
}

// hostStderr is where plugin stderr lines go; tests replace it.
var hostStderr io.Writer = os.Stderr

// Launch starts the plugin binary at path with the given args and completes the
// VPP handshake. The returned Client must be closed with Close. The plugin's
// stderr reaches the host's stderr line by line, prefixed with
// "[plugin NAME] " and redacted (structural patterns only; plugins loaded
// through a Manager use the Manager's redactor).
func Launch(ctx context.Context, path string, args ...string) (*Client, error) {
	return launch(ctx, path, args, newStderrWriter(hostStderr, filepath.Base(path), newRedactorRef()))
}

func launch(ctx context.Context, path string, args []string, stderr *stderrWriter) (*Client, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stderr = stderr
	// Bound Wait if a plugin's own children keep stderr open after it exits.
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, verr.Wrap(verr.CodePlugin, err, "open plugin stdin")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, verr.Wrap(verr.CodePlugin, err, "open plugin stdout")
	}
	if err := cmd.Start(); err != nil {
		return nil, verr.Wrap(verr.CodePlugin, err, "start plugin "+path)
	}

	c := &Client{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), Timeout: 30 * time.Second, stderr: stderr, name: filepath.Base(path)}

	// Read the handshake line (with a timeout so a bad binary can't hang us).
	hsCh := make(chan error, 1)
	go func() {
		line, rerr := c.stdout.ReadBytes('\n')
		if rerr != nil {
			hsCh <- rerr
			return
		}
		hsCh <- json.Unmarshal(line, &c.hs)
	}()
	// abandon kills a plugin that failed its handshake and reaps it, so no
	// zombie process or open pipe is left behind.
	abandon := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		stderr.Flush()
	}
	select {
	case <-time.After(10 * time.Second):
		abandon()
		return nil, verr.New(verr.CodePlugin, "plugin handshake timed out")
	case err := <-hsCh:
		if err != nil {
			abandon()
			return nil, verr.Wrap(verr.CodePlugin, err, "read plugin handshake")
		}
	}

	if c.hs.Magic != MagicCookie {
		abandon()
		return nil, verr.New(verr.CodePlugin, "invalid plugin handshake magic")
	}
	if c.hs.Version != ProtocolVersion {
		abandon()
		return nil, verr.Newf(verr.CodePlugin,
			"plugin speaks VPP/%d but host requires VPP/%d", c.hs.Version, ProtocolVersion)
	}
	return c, nil
}

// Handshake returns the plugin's advertised handshake metadata.
func (c *Client) Handshake() Handshake { return c.hs }

// call performs one RPC and returns the result map or a structured error.
func (c *Client) call(method Method, params map[string]any) (map[string]any, error) {
	return c.callTimeout(context.Background(), method, params, 0)
}

// callTimeout is call with an explicit read deadline. timeout <= 0 uses
// Client.Timeout (or 30s). Protocol execute passes the step timeout plus a
// grace period so a gRPC deadline can be returned as a response. A cancelled
// ctx returns before the write when the call has not started, and abandons
// an in-flight read the same way a timeout does.
func (c *Client) callTimeout(ctx context.Context, method Method, params map[string]any, timeout time.Duration) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, verr.Wrap(verr.CodePlugin, err, "plugin call cancelled")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.dead {
		return nil, c.deadError()
	}
	if err := ctx.Err(); err != nil {
		return nil, verr.Wrap(verr.CodePlugin, err, "plugin call cancelled")
	}

	if timeout <= 0 {
		timeout = c.Timeout
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	c.nextID++
	req := Request{ID: c.nextID, Method: method, Params: params}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, verr.Wrap(verr.CodePlugin, err, "marshal plugin request")
	}
	if err := ctx.Err(); err != nil {
		return nil, verr.Wrap(verr.CodePlugin, err, "plugin call cancelled")
	}
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		c.markDead("writing a request to it failed (the process had exited)")
		return nil, verr.Wrap(verr.CodePlugin, err, "write plugin request")
	}

	type readResult struct {
		line []byte
		err  error
	}
	resCh := make(chan readResult, 1)
	go func() {
		line, err := c.stdout.ReadBytes('\n')
		resCh <- readResult{line: line, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var line []byte
	select {
	case <-ctx.Done():
		c.markDead(fmt.Sprintf("it was killed when its %s call was cancelled", method))
		return nil, verr.Wrap(verr.CodePlugin, ctx.Err(), "plugin call cancelled")
	case <-timer.C:
		c.markDead(fmt.Sprintf("it was killed after its %s call timed out after %v", method, timeout))
		return nil, verr.Newf(verr.CodePlugin, "plugin %q call %s timed out after %v; the plugin was killed", c.name, method, timeout).
			WithDetail("plugin", c.name)
	case res := <-resCh:
		if res.err != nil {
			c.markDead("it exited or closed its output")
			return nil, verr.Wrap(verr.CodePlugin, res.err, "read plugin response")
		}
		line = res.line
	}

	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, verr.Wrap(verr.CodePlugin, err, "decode plugin response")
	}
	if resp.ID != req.ID {
		// The stream is out of step: every later answer would belong to
		// another request. Stop using this plugin process.
		c.markDead(fmt.Sprintf("it was killed after answering request %d with response id %d", req.ID, resp.ID))
		return nil, verr.Newf(verr.CodePlugin, "plugin %q answered request %d with response id %d", c.name, req.ID, resp.ID)
	}
	if resp.Error != nil {
		return nil, &verr.Error{
			Code:    verr.Code(resp.Error.Code),
			Message: resp.Error.Message,
			Details: resp.Error.Details,
		}
	}
	return resp.Result, nil
}

// Close shuts the plugin down gracefully, then waits for the process to exit.
func (c *Client) Close() error {
	c.mu.Lock()
	dead := c.dead
	c.mu.Unlock()

	if !dead {
		_, _ = c.call(MethodShutdown, nil)
	}
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		done := make(chan error, 1)
		go func() { done <- c.cmd.Wait() }()
		select {
		case <-time.After(5 * time.Second):
			_ = c.cmd.Process.Kill()
			return fmt.Errorf("plugin did not exit; killed")
		case err := <-done:
			if c.stderr != nil {
				c.stderr.Flush()
			}
			return err
		}
	}
	return nil
}
