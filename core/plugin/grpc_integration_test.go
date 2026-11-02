package plugin_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/plugin"
	"github.com/pgodlews/verity-api/core/runner"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

// TestGRPCPluginRunsSteps builds plugins/grpc and drives unary and streaming
// steps through the real VPP/1 host. The core module does not import grpc-go; the
// fixture server is a separate binary from the plugin module.
func TestGRPCPluginRunsSteps(t *testing.T) {
	if testing.Short() {
		t.Skip("skips subprocess build in -short mode")
	}
	pluginDir, err := filepath.Abs(filepath.Join("..", "..", "plugins", "grpc"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pluginDir); err != nil {
		t.Skipf("grpc plugin not present: %v", err)
	}

	binDir := t.TempDir()
	pluginBin := filepath.Join(binDir, "grpc")
	serverBin := filepath.Join(binDir, "testserver")
	buildOrSkip(t, pluginDir, pluginBin, ".")
	buildOrSkip(t, filepath.Join(pluginDir, "internal", "testserver"), serverBin, ".")

	proj, err := os.MkdirTemp("", "vgrpc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(proj) })
	sock := filepath.Join(proj, "echo.sock")
	desc := filepath.Join(proj, "echo.pb")
	startEchoServer(t, serverBin, sock, desc)

	const secret = "grpc-plugin-token-redact-me"
	t.Setenv("API_TOKEN", secret)

	reg := bootstrap.Default()
	mgr, err := plugin.Load(context.Background(), reg, []plugin.Manifest{{Name: "grpc", Path: pluginBin}})
	if err != nil {
		t.Fatalf("load plugin: %v", err)
	}
	defer mgr.Close()

	project := &model.Project{
		SchemaVersion: "1.0",
		Name:          "grpc plugin",
		SourceDir:     proj,
		Environments: []model.Environment{{
			Name: "local", Default: true,
			Variables: map[string]string{"TARGET": "unix://" + sock},
		}},
		Suites: []model.Suite{{
			Name: "echo",
			Cases: []model.TestCase{{
				Name: "unary",
				Steps: []model.Step{
					{
						Name: "echo", Protocol: "grpc",
						Request: map[string]any{
							"target": "${TARGET}", "method": "verity.testpb.Echo/Echo",
							"plaintext": true,
							"message":   map[string]any{"message": "hello", "n": 2},
							"metadata":  map[string]any{"x-token": "${env:API_TOKEN}"},
						},
						Assertions: []model.Assertion{
							{Type: "status", Value: 0},
							{Type: "jsonpath", Expr: "$.message", Value: "hello"},
							{Type: "jsonpath", Expr: "$.n", Value: 2},
							{Type: "header", Expr: "Grpc-Status", Value: "0"},
						},
					},
					{
						Name: "not found is a response", Protocol: "grpc",
						Request: map[string]any{
							"target": "${TARGET}", "method": "verity.testpb.Echo/Fail",
							"plaintext": true, "message": map[string]any{},
						},
						Assertions: []model.Assertion{{Type: "status", Value: 0}},
					},
					{
						Name: "deadline", Protocol: "grpc", TimeoutMs: 800,
						Request: map[string]any{
							"target": "${TARGET}", "method": "verity.testpb.Echo/Wait",
							"plaintext": true, "message": map[string]any{},
						},
						Assertions: []model.Assertion{{Type: "status", Value: 4}},
					},
					{
						Name: "descriptor set", Protocol: "grpc",
						Request: map[string]any{
							"target": "${TARGET}", "method": "verity.testpb.Echo/Echo",
							"plaintext": true, "descriptorSet": "echo.pb",
							"message": map[string]any{"message": "from-file"},
						},
						Assertions: []model.Assertion{
							{Type: "status", Value: 0},
							{Type: "jsonpath", Expr: "$.message", Value: "from-file"},
						},
					},
					{
						Name: "server stream", Protocol: "grpc",
						Request: map[string]any{
							"target": "${TARGET}", "method": "verity.testpb.Echo/ServerStream", "plaintext": true,
							"message":  map[string]any{"message": "streamed"},
							"metadata": map[string]any{"authorization": "${env:API_TOKEN}"},
						},
						Assertions: []model.Assertion{
							{Type: "status", Value: 0},
							{Type: "jsonpath", Expr: "$[0].message", Value: "streamed"},
							{Type: "jsonpath", Expr: "$[2].n", Value: 3},
							{Type: "header", Expr: "messageCount", Value: "3"},
							{Type: "header", Expr: "truncated", Value: "false"},
						},
					},
					{
						Name: "client stream", Protocol: "grpc",
						Request: map[string]any{
							"target": "${TARGET}", "method": "verity.testpb.Echo/ClientStream", "plaintext": true,
							"messages": []any{map[string]any{"message": "a"}, map[string]any{"message": "b"}},
						},
						Assertions: []model.Assertion{
							{Type: "status", Value: 0},
							{Type: "jsonpath", Expr: "$[0].message", Value: "a,b"},
							{Type: "header", Expr: "messageCount", Value: "1"},
						},
					},
					{
						Name: "bidi stream", Protocol: "grpc",
						Request: map[string]any{
							"target": "${TARGET}", "method": "verity.testpb.Echo/Stream", "plaintext": true,
							"messages": []any{map[string]any{"message": "a"}, map[string]any{"message": "b"}},
							"metadata": map[string]any{"x-token": "${env:API_TOKEN}"},
						},
						Assertions: []model.Assertion{
							{Type: "status", Value: 0},
							{Type: "jsonpath", Expr: "$[1].message", Value: "b"},
							{Type: "header", Expr: "messageCount", Value: "2"},
						},
					},
				},
			}},
		}},
	}

	res, err := runner.New(reg).Run(context.Background(), project, runner.Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Suites) != 1 || len(res.Suites[0].Cases) != 1 || len(res.Suites[0].Cases[0].Steps) != 7 {
		t.Fatalf("shape: %+v", res.Suites)
	}
	steps := res.Suites[0].Cases[0].Steps
	if steps[0].Status != spi.StatusPassed {
		t.Fatalf("echo: %+v", steps[0])
	}
	if steps[0].Response == nil || !strings.Contains(steps[0].Response.Body, secrets.Mask) {
		t.Fatalf("metadata echo was not redacted: %+v", steps[0].Response)
	}
	redactedHeader := false
	for _, v := range steps[0].Request.Headers {
		if strings.Contains(v, secrets.Mask) {
			redactedHeader = true
		}
	}
	if !redactedHeader {
		t.Fatalf("request metadata was not redacted: %+v", steps[0].Request)
	}
	if steps[1].Status != spi.StatusFailed || steps[1].Error != nil || steps[1].Response == nil || steps[1].Response.StatusCode != 5 {
		t.Fatalf("server status should fail an assertion, not the transport: %+v", steps[1])
	}
	if steps[2].Status != spi.StatusPassed || steps[2].Response == nil || steps[2].Response.StatusCode != 4 {
		t.Fatalf("deadline: %+v", steps[2])
	}
	if steps[2].Response.DurationMS < 100 {
		t.Fatalf("deadline duration %dms", steps[2].Response.DurationMS)
	}
	if steps[3].Status != spi.StatusPassed {
		t.Fatalf("descriptor set: %+v", steps[3])
	}

	for _, i := range []int{4, 5, 6} {
		if steps[i].Status != spi.StatusPassed {
			t.Fatalf("streaming step %d: %+v", i, steps[i])
		}
	}
	for _, i := range []int{4, 6} {
		if steps[i].Response == nil || !strings.Contains(steps[i].Response.Body, secrets.Mask) {
			t.Fatalf("streamed metadata not redacted: %+v", steps[i].Response)
		}
		masked := false
		for _, value := range steps[i].Request.Headers {
			masked = masked || strings.Contains(value, secrets.Mask)
		}
		if !masked {
			t.Fatalf("stream request metadata not redacted: %+v", steps[i].Request)
		}
	}

	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("secret leaked into the run result")
	}
}

func buildOrSkip(t *testing.T, dir, out string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", append([]string{"build", "-o", out}, args...)...)
	cmd.Dir = dir
	outb, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	var ee *exec.Error
	text := string(outb)
	if errors.As(err, &ee) || strings.Contains(text, "cannot find module") ||
		strings.Contains(text, "network is unreachable") || strings.Contains(text, "dial tcp") {
		t.Skipf("build not possible: %v\n%s", err, outb)
	}
	t.Fatalf("build %s: %v\n%s", dir, err, outb)
}

func startEchoServer(t *testing.T, bin, sock, desc string) {
	t.Helper()
	cmd := exec.Command(bin, "-listen", sock, "-descriptor-out", desc)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	done := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		if sc.Scan() && strings.TrimSpace(sc.Text()) == "ready" {
			done <- nil
			return
		}
		if err := sc.Err(); err != nil {
			done <- err
			return
		}
		done <- fmt.Errorf("server exited before ready: %s", stderr.String())
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("echo server: %v\n%s", err, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("echo server did not become ready\n%s", stderr.String())
	}
}
