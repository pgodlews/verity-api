package load

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/runner"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

func TestAllowedIP(t *testing.T) {
	cases := []struct {
		ip string
		ok bool
	}{
		{"127.0.0.1", true},
		{"127.255.255.255", true},
		{"::1", true},
		{"10.1.2.3", true},
		{"192.168.0.1", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"169.254.1.1", true},
		{"fe80::1", true},
		{"::ffff:10.0.0.1", true},
		{"::ffff:127.0.0.1", true},
		{"fd12:3456::1", true}, // IPv6 unique-local (fc00::/7)
		{"fc00::1", true},
		{"fe00::1", false},
		{"172.15.0.1", false},
		{"172.32.0.1", false},
		{"11.0.0.1", false},
		{"203.0.113.10", false},
		{"192.0.2.1", false},
		{"8.8.8.8", false},
		{"2001:db8::1", false},
		{"::ffff:203.0.113.10", false},
	}
	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("bad ip %s", tc.ip)
		}
		if got := allowedIP(ip); got != tc.ok {
			t.Errorf("allowedIP(%s) = %v, want %v", tc.ip, got, tc.ok)
		}
	}
}

func TestHostAllowedUsesLookupForNamesOnly(t *testing.T) {
	looked := false
	lookup := func(host string) ([]net.IP, error) {
		looked = true
		if host != "name.invalid" {
			t.Errorf("lookup %s", host)
		}
		return []net.IP{net.ParseIP("203.0.113.10")}, nil
	}
	if hostAllowed("203.0.113.10", lookup) {
		t.Fatal("public literal should be refused")
	}
	if looked {
		t.Fatal("IP literal was resolved")
	}
	if hostAllowed("name.invalid", lookup) {
		t.Fatal("name resolving to a public address should be refused")
	}
	if !looked {
		t.Fatal("name was not resolved")
	}
	if !hostAllowed("loop.invalid", func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}) {
		t.Fatal("name resolving only to loopback should be allowed")
	}
	if hostAllowed("missing.invalid", func(string) ([]net.IP, error) {
		return nil, errors.New("no such host")
	}) {
		t.Fatal("unresolvable name should be refused")
	}
	if hostAllowed("${BASE}", func(string) ([]net.IP, error) {
		t.Fatal("unresolved template should not be looked up")
		return nil, nil
	}) {
		t.Fatal("unresolved template should be refused")
	}
}

func TestScreenRefusesPublicLiteralWithoutLookup(t *testing.T) {
	p := &model.Project{
		SchemaVersion: "1.0",
		Name:          "remote",
		Environments:  []model.Environment{{Name: "local", Default: true}},
		Suites: []model.Suite{{
			Name: "s",
			Cases: []model.TestCase{{
				Name: "c",
				Steps: []model.Step{{
					Name: "get", Protocol: "rest",
					Request: map[string]any{"url": "http://203.0.113.10/nope"},
				}},
			}},
		}},
	}
	exec, err := runner.New(bootstrap.Default()).Prepare(p, runner.Options{})
	if err != nil {
		t.Fatal(err)
	}
	lookups := 0
	lookup := func(string) ([]net.IP, error) {
		lookups++
		return nil, errors.New("should not be called")
	}
	if _, err := screenTargets(exec, false, lookup); err == nil {
		t.Fatal("expected refusal")
	} else if !strings.Contains(err.Error(), "203.0.113.10") || !strings.Contains(err.Error(), "allow-remote") {
		t.Fatalf("error %v", err)
	}
	if lookups != 0 {
		t.Fatal("resolved an IP literal")
	}
	hosts, err := screenTargets(exec, true, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0] != "203.0.113.10" {
		t.Fatalf("hosts %v", hosts)
	}
	if lookups != 0 {
		t.Fatal("allow-remote still should not resolve a literal")
	}
}

func TestScreenFeedRowOverridesHost(t *testing.T) {
	p := &model.Project{
		SchemaVersion: "1.0",
		Name:          "feed",
		Environments: []model.Environment{{
			Name: "local", Default: true,
			Variables: map[string]string{"host": "127.0.0.1"},
		}},
		Suites: []model.Suite{{
			Name: "s",
			Cases: []model.TestCase{{
				Name: "hit",
				Feed: &model.Feed{
					Type:   "json",
					Inline: []map[string]string{{"host": "203.0.113.10"}},
				},
				Steps: []model.Step{{
					Name: "get", Protocol: "rest",
					Request: map[string]any{"method": "GET", "url": "http://${host}/"},
				}},
			}},
		}},
	}
	exec, err := runner.New(bootstrap.Default()).Prepare(p, runner.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = screenTargets(exec, false, func(string) ([]net.IP, error) {
		t.Fatal("IP literal was resolved")
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "203.0.113.10") || !strings.Contains(err.Error(), "target") {
		t.Fatalf("error %v", err)
	}
}

func TestScreenFeedLoadFailure(t *testing.T) {
	p := &model.Project{
		SchemaVersion: "1.0",
		Name:          "feed",
		Environments:  []model.Environment{{Name: "local", Default: true}},
		Suites: []model.Suite{{
			Name: "s",
			Cases: []model.TestCase{{
				Name: "hit",
				Feed: &model.Feed{Type: "missing"},
				Steps: []model.Step{{
					Name: "get", Protocol: "rest",
					Request: map[string]any{"url": "http://127.0.0.1/"},
				}},
			}},
		}},
	}
	exec, err := runner.New(bootstrap.Default()).Prepare(p, runner.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = screenTargets(exec, false, func(string) ([]net.IP, error) {
		t.Fatal("should not resolve")
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error %v", err)
	}
}

func TestScreenDNSAndPassthroughPath(t *testing.T) {
	for _, raw := range []string{
		"dns://127.0.0.1/203.0.113.10:443",
		"passthrough://127.0.0.1/203.0.113.10:443",
	} {
		p := &model.Project{
			SchemaVersion: "1.0",
			Name:          "dial",
			Environments:  []model.Environment{{Name: "local", Default: true}},
			Suites: []model.Suite{{
				Name: "s",
				Cases: []model.TestCase{{
					Name: "c",
					Steps: []model.Step{{
						Name: "call", Protocol: "grpc",
						Request: map[string]any{"target": raw},
					}},
				}},
			}},
		}
		exec, err := runner.New(bootstrap.Default()).Prepare(p, runner.Options{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = screenTargets(exec, false, func(string) ([]net.IP, error) {
			t.Fatal("literal was resolved")
			return nil, nil
		})
		if err == nil || !strings.Contains(err.Error(), "203.0.113.10") {
			t.Fatalf("%s: %v", raw, err)
		}
	}
}

func TestScreenPluginStep(t *testing.T) {
	reg := bootstrap.Default()
	reg.RegisterProtocol(hiddenProto{})
	bare := &model.Project{
		SchemaVersion: "1.0",
		Name:          "plugin",
		Environments:  []model.Environment{{Name: "local", Default: true}},
		Suites: []model.Suite{{
			Name: "s",
			Cases: []model.TestCase{{
				Name: "c",
				Steps: []model.Step{{
					Name: "send", Protocol: "custom",
					Request: map[string]any{"method": "Echo"},
				}},
			}},
		}},
	}
	exec, err := runner.New(reg).Prepare(bare, runner.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = screenTargets(exec, false, nil)
	if err == nil || !strings.Contains(err.Error(), "send") || !strings.Contains(err.Error(), "allow-remote") {
		t.Fatalf("bare plugin step: %v", err)
	}
	if _, err := screenTargets(exec, true, nil); err != nil {
		t.Fatalf("allow-remote: %v", err)
	}

	reg = bootstrap.Default()
	reg.RegisterProtocol(hiddenProto{})
	hosted := &model.Project{
		SchemaVersion: "1.0",
		Name:          "plugin",
		Environments:  []model.Environment{{Name: "local", Default: true}},
		Suites: []model.Suite{{
			Name: "s",
			Cases: []model.TestCase{{
				Name: "c",
				Steps: []model.Step{{
					Name: "send", Protocol: "custom",
					Request: map[string]any{"host": "203.0.113.10"},
				}},
			}},
		}},
	}
	exec, err = runner.New(reg).Prepare(hosted, runner.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = screenTargets(exec, false, func(string) ([]net.IP, error) {
		t.Fatal("literal was resolved")
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "203.0.113.10") {
		t.Fatalf("plugin host: %v", err)
	}
}

type hiddenProto struct{}

func (hiddenProto) Type() string { return "custom" }
func (hiddenProto) DialHidden()  {}
func (hiddenProto) BuildRequest(context.Context, map[string]any) (*spi.Request, error) {
	return &spi.Request{}, nil
}

func (hiddenProto) Execute(context.Context, *spi.ProtocolContext, *spi.Request) (*spi.Response, error) {
	return &spi.Response{}, nil
}

func TestEndpointHosts(t *testing.T) {
	cases := []struct {
		raw   string
		hosts []string
		local bool
	}{
		{"http://127.0.0.1:9/v", []string{"127.0.0.1"}, false},
		{"http://[::1]:9/v", []string{"::1"}, false},
		{"unix://socket", nil, true},
		{"dns:///127.0.0.1:9", []string{"127.0.0.1"}, false},
		{"passthrough:///127.0.0.1:9", []string{"127.0.0.1"}, false},
		{"dns://127.0.0.1/203.0.113.10:443", []string{"127.0.0.1", "203.0.113.10"}, false},
		{"passthrough://127.0.0.1/203.0.113.10:443", []string{"127.0.0.1", "203.0.113.10"}, false},
	}
	for _, tc := range cases {
		hosts, local, err := endpointHosts(tc.raw)
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		if local != tc.local || strings.Join(hosts, ",") != strings.Join(tc.hosts, ",") {
			t.Errorf("%s -> %q local=%v, want %q local=%v", tc.raw, hosts, local, tc.hosts, tc.local)
		}
	}
}

// Target discovery uses throwaway resolvers; their secrets must still reach
// the execution redactor before URL errors, refusals, or startup callbacks.
type targetKeychain struct{ value string }

func (f targetKeychain) Get(string, string) (string, error) { return f.value, nil }
func (f targetKeychain) Set(string, string, string) error   { return nil }
func (f targetKeychain) Delete(string, string) error        { return nil }

func TestSecretTargetsRedacted(t *testing.T) {
	for _, source := range []string{"env", "keychain"} {
		for _, value := range []string{"bad host-sekret", "203.0.113.10", "127.0.0.1"} {
			t.Run(source+"/"+value, func(t *testing.T) {
				t.Setenv("LOAD_TARGET_SECRET", value)
				ref := "${env:LOAD_TARGET_SECRET}"
				if source == "keychain" {
					secrets.RegisterExternalProvider("keychain", &secrets.Keychain{Backend: targetKeychain{value}})
					t.Cleanup(func() { secrets.RegisterExternalProvider("keychain", &secrets.Keychain{}) })
					ref = "${secret:keychain:project/LOAD_TARGET_SECRET}"
				}
				p := &model.Project{
					SchemaVersion: "1.0", Name: "targets",
					Environments: []model.Environment{{Name: "local", Default: true}},
					Suites:       []model.Suite{{Name: "s", Cases: []model.TestCase{{Name: "c", Steps: []model.Step{{Name: "get", Protocol: "rest", Request: map[string]any{"url": "http://" + ref + "/x"}}}}}}},
				}
				ctx, cancel := context.WithCancel(context.Background())
				cancel() // Screen targets and call BeforeStart, but send no traffic.
				var shown []string
				_, err := Run(ctx, bootstrap.Default(), p, Options{VUs: 1, Iterations: 1, BeforeStart: func(hosts []string) { shown = hosts }})
				if value == "127.0.0.1" {
					if err != nil || len(shown) != 1 || shown[0] != secrets.Mask {
						t.Fatalf("unsafe target list: %v, %v", shown, err)
					}
				} else {
					if err == nil {
						t.Fatal("expected target error")
					}
					data, _ := json.Marshal(err)
					if strings.Contains(err.Error()+string(data), value) || !strings.Contains(err.Error(), secrets.Mask) {
						t.Fatalf("unsafe target error: %v", err)
					}
				}
			})
		}
	}
}

func TestCheckTargetsReportsRefusedWithoutTraffic(t *testing.T) {
	p := &model.Project{
		SchemaVersion: "1.0",
		Name:          "targets",
		Environments:  []model.Environment{{Name: "local", Default: true}},
		Suites: []model.Suite{{
			Name: "s",
			Cases: []model.TestCase{
				{Name: "private", Steps: []model.Step{{Name: "a", Protocol: "rest", Request: map[string]any{"url": "http://127.0.0.1:8080/ok"}}}},
				{Name: "public", Steps: []model.Step{{Name: "b", Protocol: "rest", Request: map[string]any{"url": "http://203.0.113.10/nope"}}}},
			},
		}},
	}
	rep, err := CheckTargets(bootstrap.Default(), p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Hosts) != 2 || len(rep.Refused) != 1 || rep.Refused[0] != "203.0.113.10" {
		t.Fatalf("report %+v", rep)
	}
	if !strings.Contains(rep.Message, "refusing non-private target 203.0.113.10; pass --allow-remote to run it") {
		t.Fatalf("message %q", rep.Message)
	}

	onlyPrivate, err := CheckTargets(bootstrap.Default(), p, Options{Cases: []string{"private"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyPrivate.Hosts) != 1 || len(onlyPrivate.Refused) != 0 || onlyPrivate.Message != "" {
		t.Fatalf("onlyPrivate %+v", onlyPrivate)
	}
}

// The desktop preview (CheckTargets) must mask a host that came from a
// secret, in the host lists and in errors, exactly like the CLI screen.
func TestCheckTargetsRedactsSecretHosts(t *testing.T) {
	for _, value := range []string{"10.9.8.7", "203.0.113.10", "bad host-sekret"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("PREVIEW_TARGET_SECRET", value)
			p := &model.Project{
				SchemaVersion: "1.0", Name: "targets",
				Environments: []model.Environment{{Name: "local", Default: true}},
				Suites:       []model.Suite{{Name: "s", Cases: []model.TestCase{{Name: "c", Steps: []model.Step{{Name: "get", Protocol: "rest", Request: map[string]any{"url": "http://${env:PREVIEW_TARGET_SECRET}/x"}}}}}}},
			}
			rep, err := CheckTargets(bootstrap.Default(), p, Options{})
			var blob []byte
			if rep != nil {
				blob, _ = json.Marshal(rep)
			}
			if err != nil {
				blob = append(blob, err.Error()...)
			}
			if strings.Contains(string(blob), value) {
				t.Fatalf("preview leaked the secret host: %s", blob)
			}
			if !strings.Contains(string(blob), secrets.Mask) {
				t.Fatalf("preview did not show the masked host: %s", blob)
			}
		})
	}
}
