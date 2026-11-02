package importers

import (
	"crypto/rand"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
)

func TestPostmanOAuth2Grants(t *testing.T) {
	for _, grant := range []string{"client_credentials", "password_credentials", "password", "authorization_code", "authorization_code_with_pkce", "refresh_token", "implicit", ""} {
		for _, templates := range []bool{false, true} {
			name := grant + "/literal"
			if templates {
				name = grant + "/references"
			}
			t.Run(name, func(t *testing.T) {
				clientSecret, password, access := rand.Text(), rand.Text(), rand.Text()
				params := map[string]string{
					"grant_type": grant, "accessTokenUrl": "{{TOKEN_URL}}", "clientId": "{{CLIENT_ID}}",
					"authUrl": "{{AUTH_URL}}", "refreshToken": access, "clientSecret": clientSecret, "username": "{{USERNAME}}", "password": password,
					"accessToken": access, "scope": "{{SCOPE}}", "client_authentication": "body",
				}
				if templates {
					params["refreshToken"] = "{{REFRESH_TOKEN}}"
					params["clientSecret"], params["password"], params["accessToken"] = "{{CLIENT_SECRET}}", "{{PASSWORD}}", "{{ACCESS_TOKEN}}"
				}
				imp := &pmImporter{
					res: &Result{}, authNames: map[string]string{}, envLits: map[string]string{},
					warned: map[string]bool{}, captured: map[string]bool{},
				}
				a := &pmAuth{Type: "oauth2", Params: params}
				applied := imp.applyAuth(a, "request")
				if len(imp.auths) != 1 || applied.ref != imp.auths[0].Name {
					t.Fatal("missing auth definition or reference")
				}
				def := imp.auths[0]
				supported := grant != "implicit"
				wantType := "bearer"
				if supported {
					wantType = "oauth2"
					if grant == "client_credentials" {
						wantType = "oauth2_client_credentials"
					}
				}
				if def.Type != wantType {
					t.Fatalf("type: got %s, want %s", def.Type, wantType)
				}
				if supported {
					for key, want := range map[string]string{"tokenUrl": "${TOKEN_URL}", "clientId": "${CLIENT_ID}", "scope": "${SCOPE}", "authStyle": "body"} {
						if def.Config[key] != want {
							t.Errorf("incorrect mapping for %s", key)
						}
					}
					if (grant == "password" || grant == "password_credentials") && (def.Config["grant"] != "password" || def.Config["username"] != "${USERNAME}") {
						t.Error("password grant or username mapping is incorrect")
					}
				}
				fields := map[string]string{"token": "${ACCESS_TOKEN}"}
				if supported {
					fields = map[string]string{"clientSecret": "${CLIENT_SECRET}"}
					if grant == "password" || grant == "password_credentials" {
						fields["password"] = "${PASSWORD}"
					}
				}
				if grant == "authorization_code" || grant == "authorization_code_with_pkce" || grant == "" || grant == "refresh_token" {
					fields["refreshToken"] = "${REFRESH_TOKEN}"
					wantGrant := grant
					if grant != "refresh_token" {
						wantGrant = "authorization_code"
						if def.Config["authUrl"] != "${AUTH_URL}" {
							t.Error("missing auth URL")
						}
					}
					if def.Config["grant"] != wantGrant {
						t.Error("incorrect code/refresh grant mapping")
					}
				}
				for key, template := range fields {
					got, _ := def.Config[key].(string)
					if templates && got != template || !templates && !strings.HasPrefix(got, "${env:") {
						t.Errorf("credential %s was not converted to a reference", key)
					}
				}
				warnings := strings.Join(imp.res.Warnings, "\n")
				if strings.Contains(warnings, "not supported yet") == supported {
					t.Error("unsupported-grant warning does not match the grant")
				}
				project := &model.Project{
					SchemaVersion: "1.0", Name: "oauth import", Auth: imp.auths,
					Suites: []model.Suite{{Name: "suite", Auth: &model.AuthRef{Use: []string{applied.ref}},
						Cases: []model.TestCase{{Name: "case", Steps: []model.Step{{Name: "step", Protocol: "rest", Request: map[string]any{"url": "${BASE_URL}"}}}}}}},
				}
				if errs := project.Validate(); len(errs) != 0 {
					t.Fatalf("invalid imported auth project: %v", errs)
				}
				out, err := json.Marshal(project)
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{clientSecret, password, access} {
					if strings.Contains(string(out)+warnings, secret) {
						t.Error("literal credential leaked into project or warnings")
					}
				}
				if again := imp.applyAuth(a, "second request"); !reflect.DeepEqual(again, applied) || len(imp.auths) != 1 {
					t.Error("repeated OAuth auth was not deduplicated")
				}
			})
		}
	}
}
