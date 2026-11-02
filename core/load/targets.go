package load

import (
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/plugin"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/runner"
	"github.com/pgodlews/verity-api/core/verr"
)

// allowedIP reports whether ip is a target the load command may hit without
// --allow-remote: 127.0.0.0/8, ::1, RFC1918, or link-local unicast
// (169.254.0.0/16, fe80::/10). IPv4-mapped IPv6 addresses are judged as IPv4.
func allowedIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return true
	}
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	if v4[0] == 10 {
		return true
	}
	if v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31 {
		return true
	}
	if v4[0] == 192 && v4[1] == 168 {
		return true
	}
	return false
}

// httpEndpointKeys are the request fields an in-process HTTP step dials.
var httpEndpointKeys = []string{"url", "target"}

// pluginEndpointKeys are the request fields an out-of-process protocol may
// dial. The host process never sees that socket, so each one is screened.
var pluginEndpointKeys = []string{"target", "url", "address", "endpoint", "host"}

// collectHosts returns the sorted, unique hosts of every selected step,
// including suite setup hooks and every feed row. unix targets are local
// and omitted. Relative URLs and empty endpoints are a config error. A feed
// that fails to load fails the screen.
func collectHosts(x *runner.CaseExec, allowRemote bool) ([]string, error) {
	p := x.Project()
	seen := map[string]struct{}{}
	var hosts []string
	addHost := func(host string) {
		if host == "" {
			return
		}
		if _, ok := seen[host]; ok {
			return
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	addStep := func(suite *model.Suite, c *model.TestCase, step *model.Step, row map[string]string) error {
		if step == nil || step.Skip {
			return nil
		}
		raws, err := endpointRaws(x, step, allowRemote)
		if err != nil {
			return err
		}
		for _, raw := range raws {
			interp, err := x.InterpolateRequest(suite, c, step, row, raw)
			if err != nil {
				return err
			}
			found, local, err := endpointHosts(interp)
			if err != nil {
				return err
			}
			if local {
				continue
			}
			for _, h := range found {
				addHost(h)
			}
		}
		return nil
	}
	for i := range p.Suites {
		s := &p.Suites[i]
		if s.Setup != nil {
			for j := range s.Setup.Steps {
				if err := addStep(s, nil, &s.Setup.Steps[j], nil); err != nil {
					return nil, err
				}
			}
		}
		for j := range s.Cases {
			c := &s.Cases[j]
			rows, err := x.FeedRows(c)
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				for k := range c.Steps {
					if err := addStep(s, c, &c.Steps[k], row); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	sort.Strings(hosts)
	return hosts, nil
}

// endpointRaws lists the address strings a step may dial. An out-of-process
// protocol with none of the known keys is refused unless allowRemote is set:
// the dial guard cannot see that process.
func endpointRaws(x *runner.CaseExec, step *model.Step, allowRemote bool) ([]string, error) {
	hides := true
	if p, err := x.Protocol(step.Protocol); err == nil {
		hides = plugin.HidesDial(p)
	}
	keys := httpEndpointKeys
	if hides {
		keys = pluginEndpointKeys
	}
	var raws []string
	if step.Request != nil {
		for _, key := range keys {
			if v, ok := step.Request[key].(string); ok && strings.TrimSpace(v) != "" {
				raws = append(raws, v)
			}
		}
	}
	if hides && len(raws) == 0 && !allowRemote {
		return nil, verr.Newf(verr.CodeConfig,
			"refusing plugin step %q: no target, url, address, endpoint, or host to screen; pass --allow-remote",
			step.Name)
	}
	return raws, nil
}

// endpointHosts extracts the hosts from an HTTP URL or a gRPC dial target.
// local is true for unix sockets, which never leave the machine. dns and
// passthrough put the dial target in the path and may also name an authority.
func endpointHosts(raw string) (hosts []string, local bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false, verr.New(verr.CodeConfig, "request URL is empty")
	}
	if strings.HasPrefix(raw, "unix:") {
		return nil, true, nil
	}
	if !strings.Contains(raw, "://") {
		return []string{hostOnly(raw)}, false, nil
	}
	u, perr := url.Parse(raw)
	if perr != nil {
		return nil, false, verr.Newf(verr.CodeConfig, "parse request URL %q: %v", raw, perr)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "dns" || scheme == "passthrough" {
		if h := u.Hostname(); h != "" {
			hosts = append(hosts, h)
		}
		if path := dialPath(u); path != "" {
			hosts = append(hosts, hostOnly(path))
		}
		if len(hosts) == 0 {
			return nil, false, verr.Newf(verr.CodeConfig, "request URL %q has no host", raw)
		}
		return hosts, false, nil
	}
	if h := u.Hostname(); h != "" {
		return []string{h}, false, nil
	}
	// grpc forms such as dns:///host:port are handled above. Anything else
	// with an empty host and a path is still a dial target.
	if path := dialPath(u); path != "" {
		return []string{hostOnly(path)}, false, nil
	}
	return nil, false, verr.Newf(verr.CodeConfig, "request URL %q has no host", raw)
}

func dialPath(u *url.URL) string {
	path := strings.TrimPrefix(u.EscapedPath(), "/")
	if i := strings.IndexByte(path, '/'); i >= 0 {
		path = path[:i]
	}
	return path
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// hostAllowed reports whether host is a loopback, RFC1918, or link-local
// address. IP literals are judged without a lookup. Other names are resolved
// with lookup; every returned address must be allowed. An unresolvable name
// is not allowed unless allowRemote is set (the caller still refuses when
// allowRemote is false).
func hostAllowed(host string, lookup func(string) ([]net.IP, error), allowRemote bool) bool {
	if ip := net.ParseIP(host); ip != nil {
		return allowedIP(ip)
	}
	if strings.Contains(host, "${") || strings.Contains(host, "}") {
		return false
	}
	ips, err := lookup(host)
	if err != nil || len(ips) == 0 {
		return allowRemote
	}
	for _, ip := range ips {
		if !allowedIP(ip) {
			return false
		}
	}
	return true
}

// screenTargets collects hosts and refuses any that are not private unless
// allowRemote is set. The returned list is every host, including refused
// ones, so the caller can print them. lookup is net.LookupIP when nil.
// Hosts in the list and in the error are passed through the run redactor
// (a host can come from ${env:NAME}); the check itself uses the raw names.
func screenTargets(x *runner.CaseExec, allowRemote bool, lookup func(string) ([]net.IP, error)) ([]string, error) {
	hosts, refused, err := classifyTargets(x, allowRemote, lookup)
	if err != nil {
		return nil, err
	}
	if len(refused) > 0 && !allowRemote {
		return hosts, refusalError(refused)
	}
	return hosts, nil
}

// classifyTargets returns every selected host and the ones outside the
// private allow-list, both redacted for display.
func classifyTargets(x *runner.CaseExec, allowRemote bool, lookup func(string) ([]net.IP, error)) (hosts, refused []string, err error) {
	// Interpolation registers resolved secrets with the run redactor; an
	// error can still quote one (an invalid host from ${env:NAME}).
	defer func() { err = x.Redactor().RedactError(err) }()
	if lookup == nil {
		lookup = net.LookupIP
	}
	raw, err := collectHosts(x, allowRemote)
	if err != nil {
		return nil, nil, err
	}
	red := x.Redactor()
	hosts = make([]string, 0, len(raw))
	refused = []string{}
	for _, h := range raw {
		shown := h
		if red != nil {
			shown = red.Redact(h)
		}
		hosts = append(hosts, shown)
		if !hostAllowed(h, lookup, false) {
			refused = append(refused, shown)
		}
	}
	return hosts, refused, nil
}

// refusalError is the pre-run refusal the CLI prints (exit 3) and the
// desktop app shows.
func refusalError(refused []string) *verr.Error {
	return verr.Newf(verr.CodeConfig,
		"refusing non-private target %s; pass --allow-remote to run it",
		strings.Join(refused, ", ")).WithDetail("hosts", refused)
}

// TargetReport is the pre-run target screen without any traffic. Hosts is
// every selected host, sorted and redacted. Refused lists the hosts outside
// the private allow-list, whether or not AllowRemote is set. Message is the
// refusal sentence Run would return, and is empty when AllowRemote is set
// or nothing is refused.
type TargetReport struct {
	Hosts   []string `json:"hosts"`
	Refused []string `json:"refused"`
	Message string   `json:"message,omitempty"`
}

// CheckTargets resolves the hosts Run would screen for opts and reports
// which are refused. It sends no request and runs no setup hook. The error
// is a config error for the same reasons Run refuses to start (bad filter,
// missing environment, a feed that fails to load), except a refused target,
// which is reported in the result.
func CheckTargets(reg *registry.Registry, project *model.Project, opts Options) (*TargetReport, error) {
	exec, err := prepare(reg, project, opts, nil)
	if err != nil {
		return nil, err
	}
	defer exec.CloseIdleTransports()
	hosts, refused, err := classifyTargets(exec, opts.AllowRemote, opts.LookupIP)
	if err != nil {
		return nil, err
	}
	out := &TargetReport{Hosts: hosts, Refused: refused}
	if len(refused) > 0 && !opts.AllowRemote {
		out.Message = refusalError(refused).Message
	}
	return out, nil
}
