package load

import (
	"net"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/runner"
	"github.com/pgodlews/verity-api/core/verr"
)

// A plugin step that dials a host name is refused without --allow-remote:
// the plugin process resolves it, out of the guard's reach. IP literals
// and unix sockets are screened as before.
func TestPluginStepWithHostNameNeedsAllowRemote(t *testing.T) {
	project := func(target string) *model.Project {
		return &model.Project{
			SchemaVersion: "1.0", Name: "plugin",
			Environments: []model.Environment{{Name: "local", Default: true}},
			Suites: []model.Suite{{Name: "s", Cases: []model.TestCase{{Name: "c", Steps: []model.Step{{
				Name: "send", Protocol: "custom", Request: map[string]any{"target": target},
			}}}}}},
		}
	}
	lookup := func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }
	for target, wantErr := range map[string]bool{
		"localhost:50051":         true,
		"dns:///svc.internal:443": true,
		"127.0.0.1:50051":         false,
		"unix:///tmp/plugin.sock": false,
	} {
		reg := bootstrap.Default()
		reg.RegisterProtocol(hiddenProto{})
		exec, err := runner.New(reg).Prepare(project(target), runner.Options{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = screenTargets(exec, false, lookup)
		if wantErr {
			ve, ok := verr.As(err)
			if !ok || ve.Code != verr.CodeConfig || !strings.Contains(ve.Message, "allow-remote") {
				t.Fatalf("%s: error %v", target, err)
			}
			if _, err := screenTargets(exec, true, lookup); err != nil {
				t.Fatalf("%s with allow-remote: %v", target, err)
			}
		} else if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
	}
}
