package importers

import (
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
)

func TestPostmanDigestAndPublicPKCE(t *testing.T) {
	for _, typ := range []string{"digest", "oauth2"} {
		for _, references := range []bool{false, true} {
			t.Run(typ+map[bool]string{false: "/literal", true: "/reference"}[references], func(t *testing.T) {
				password := rand.Text()
				value := password
				if references {
					value = "{{PASSWORD}}"
				}
				params := map[string]string{
					"username": "{{USER}}", "password": value, "algorithm": "SHA-256",
					"grant_type": "authorization_code_with_pkce", "authUrl": "{{AUTH_URL}}",
					"accessTokenUrl": "{{TOKEN_URL}}", "clientId": "{{CLIENT_ID}}", "refreshToken": value,
				}
				imp := &pmImporter{
					res: &Result{}, authNames: map[string]string{}, envLits: map[string]string{},
					warned: map[string]bool{}, captured: map[string]bool{},
				}
				applied := imp.applyAuth(&pmAuth{Type: typ, Params: params}, "request")
				if len(imp.auths) != 1 || applied.ref == "" {
					t.Fatal("missing definition")
				}
				def := imp.auths[0]
				if def.Type != typ {
					t.Fatal("auth type not mapped")
				}
				field := "password"
				if typ == "oauth2" {
					field = "refreshToken"
					if _, ok := def.Config["clientSecret"]; ok {
						t.Fatal("public client given a secret")
					}
					if def.Config["grant"] != "authorization_code" {
						t.Fatal("PKCE not mapped")
					}
				}
				got, _ := def.Config[field].(string)
				if references && got != "${PASSWORD}" || !references && !strings.HasPrefix(got, "${env:") {
					t.Fatal("credential not replaced by reference")
				}
				if typ == "digest" && def.Config["algorithm"] != "SHA-256" {
					t.Fatal("algorithm dropped")
				}
				encoded, _ := json.Marshal(def)
				if strings.Contains(string(encoded)+strings.Join(imp.res.Warnings, " "), password) {
					t.Fatal("secret leaked")
				}
				if strings.Contains(strings.Join(imp.res.Warnings, " "), "clientSecret") {
					t.Fatal("optional client secret warning")
				}
			})
		}
	}
}
