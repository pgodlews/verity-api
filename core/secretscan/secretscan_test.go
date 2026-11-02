package secretscan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCheck(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abcdefghijklmnop"
	tests := []struct {
		key, value string
		place      bool
		want       string // "" = not a secret
	}{
		{"password", "hunter2", false, "name"},
		{"password", "REDACTED", false, ""},
		{"password", "REDACTED-secret", false, "name"},
		{"Authorization", "REDACTED", true, ""},
		{"password", "${env:PW}", false, ""},
		{"password", "pre-${env:PW}", false, "name"},
		{"password", "", false, ""},
		{"clientSecret", "abc", false, "name"},
		{"X-Api-Key", "k-123", true, "name"},
		{"Authorization", "Bearer ${env:TOKEN}", true, ""},
		{"Authorization", "Basic ${secret:file:creds}", true, ""},
		{"Authorization", "Bearer abc.def", true, "name"},
		{"Cookie", "session=abc", true, "name"},
		{"tokenUrl", "https://idp.example/token", false, ""},
		{"passwordFile", "/run/secrets/pw", false, ""},
		{"token_endpoint", "https://x", false, ""},
		{"grant", "password", false, ""},
		{"username", "ada", false, ""},
		{"X-Request-Id", "550e8400-e29b-41d4-a716-446655440000", true, ""},
		{"X-Signature", jwt, true, "shape"},
		{"value", jwt, false, ""}, // shape only counts where credentials go
		{"X-Assertion", "abcdefghijklmnopqrstuvwxyz0123", true, "shape"},
		{"X-Trace-Id", "abcdefghijklmnopqrstuvwxyz0123", true, ""},
		{"secretAccessKey", "hunter2-secret-key", true, "name"},
		{"secretAccessKey", "${env:AWS_SECRET_ACCESS_KEY}", true, ""},
		{"secret", "hunter2-hmac-secret", true, "name"},
		{"secret", "${env:HMAC_SECRET}", true, ""},
		{"sessionToken", "hunter2-session-token", true, "name"},
		{"sessionToken", "${env:AWS_SESSION_TOKEN}", true, ""},
	}
	for _, tt := range tests {
		ok, why := Check(tt.key, tt.value, tt.place)
		if (tt.want != "") != ok || why != tt.want {
			t.Errorf("Check(%q, %q, %v) = %v %q, want %q", tt.key, tt.value, tt.place, ok, why, tt.want)
		}
	}
}

func TestSuggestEnvVar(t *testing.T) {
	for in, want := range map[string]string{
		"X-Api-Key": "X_API_KEY", "clientSecret": "CLIENT_SECRET", "password": "PASSWORD",
		"Authorization": "AUTHORIZATION", "api_key": "API_KEY", "2fa-token": "V_2FA_TOKEN", "--": "SECRET",
	} {
		if got := SuggestEnvVar(in); got != want {
			t.Errorf("SuggestEnvVar(%q) = %q, want %q", in, got, want)
		}
	}
}

const projectYAML = `schemaVersion: "1.0"
name: scan
environments:
  - name: dev
    variables:
      BASE_URL: http://localhost
      API_TOKEN: tok-literal-1
      SAFE_TOKEN: ${env:SAFE_TOKEN}
auth:
  - name: oauth
    type: oauth2
    config:
      tokenUrl: https://idp.example/token
      clientId: app
      clientSecret: s3cr3t
suites:
  - name: s
    cases:
      - name: c
        steps:
          - name: login
            protocol: rest
            request:
              url: ${BASE_URL}/login
              headers:
                Authorization: Bearer ${env:T}
                X-Api-Key: abc
              json:
                password: 1234
            assertions:
              - {type: jsonpath, expr: $.token, op: exists}
`

func TestScanFile(t *testing.T) {
	got, err := ScanFile("p.yaml", []byte(projectYAML))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range got {
		paths = append(paths, f.PathString())
		if f.Line == 0 || f.File != "p.yaml" {
			t.Errorf("finding without location: %+v", f)
		}
	}
	want := []string{
		"environments[0].variables.API_TOKEN",
		"auth[0].config.clientSecret",
		"suites[0].cases[0].steps[0].request.headers.X-Api-Key",
		"suites[0].cases[0].steps[0].request.json.password",
	}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("paths:\n%s\nwant:\n%s", strings.Join(paths, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(got[1].Message(), "${env:CLIENT_SECRET}") || strings.Contains(got[1].Message(), "s3cr3t") {
		t.Fatalf("message must suggest a reference and never echo the value: %s", got[1].Message())
	}
}

func TestScanValue(t *testing.T) {
	step := map[string]any{
		"name": "s",
		"request": map[string]any{
			"headers": map[string]any{"X-Api-Key": "abc", "Authorization": "Bearer ${env:T}"},
			"json":    map[string]any{"user": "ada", "pin": "x", "password": float64(1234)},
		},
	}
	got := ScanValue(step)
	if len(got) != 2 || got[0].PathString() != "request.headers.X-Api-Key" || got[1].PathString() != "request.json.password" {
		t.Fatalf("findings: %+v", got)
	}
}

// The shipped examples and the importer fixtures must scan clean: they use
// references, and a false positive here would fire on every user's commit.
func TestExamplesScanClean(t *testing.T) {
	root := filepath.Join("..", "..", "examples")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ext := filepath.Ext(path)
		if (ext != ".yaml" && ext != ".yml") || strings.Contains(path, string(filepath.Separator)+"specs"+string(filepath.Separator)) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		got, err := ScanFile(path, data)
		if err != nil {
			return err
		}
		for _, f := range got {
			t.Errorf("%s:%d %s", f.File, f.Line, f.Message())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func paths(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		p := f.PathString() + "#" + f.Key
		if f.InText {
			p += "(text)"
		}
		out = append(out, p)
	}
	return out
}

// Bypasses and false positives from the phase-1 review (Kimi k3.1-pro).
func TestScanReviewCases(t *testing.T) {
	tests := []struct {
		name, yaml string
		want       []string
	}{
		{"json body string", `request: {body: '{"username":"ada","password":"hunter2"}'}`,
			[]string{"request.body.password#password"}},
		{"soap body", "request:\n  body: |\n    <Envelope><wsse:Password Type=\"t\">hunter2</wsse:Password><User>ada</User></Envelope>\n",
			[]string{"request.body#Password(text)"}},
		{"soap body reference", "request:\n  body: <Password>${env:PW}</Password>\n", nil},
		{"form body", `request: {body: "user=ada&password=hunter2"}`, []string{"request.body#password(text)"}},
		{"alias to secret key", "x: &pw hunter2\nauth:\n  - config:\n      password: *pw\n",
			[]string{"auth[0].config.password#password"}},
		{"merge key", "base: &b {apiKey: abc}\nrequest:\n  headers:\n    <<: *b\n",
			[]string{"base.apiKey#apiKey", "request.headers.apiKey#apiKey"}},
		{"alias cycle", "a: &a [*a]\n", nil},
		{"second document", "schemaVersion: \"1.0\"\n---\npassword: hunter2\n", []string{"password#password"}},
		{"url userinfo and query", `request: {url: "https://user:hunter2@example.com/p?api_key=abc&q=1"}`,
			[]string{"request.url#password(text)", "request.url#api_key(text)"}},
		{"url with references", `request: {url: "https://${env:U}:${env:P}@example.com/p?api_key=${env:K}"}`, nil},
		{"name suffix holding a secret", `variables: {API_TOKEN_NAME: hunter2}`, []string{"variables.API_TOKEN_NAME#API_TOKEN_NAME"}},
		{"name suffix holding metadata", `variables: {API_TOKEN_NAME: X-Auth-Token, TOKEN_TTL: "3600", PASSWORD_PATH: /run/pw}`, nil},
		{"PWD and checksums", `variables: {PWD: /home/ada/project, CHECKSUM: 0123456789abcdef0123456789abcdef, ID: ABCDEFGHIJKLMNOPQRSTUVWXYZ}`, nil},
		// Re-check findings (N1-N10).
		{"N1 placeholder collision", `request: {url: "https://x/?api_key=VERITYREF0X&x=${env:X}"}`, []string{"request.url#api_key(text)"}},
		{"N2 xml cdata", `request: {body: '<Password><![CDATA[hunter2]]></Password>'}`, []string{"request.body#Password(text)"}},
		{"N2 xml attribute", `request: {body: '<Credentials password="hunter2"/>'}`, []string{"request.body#password(text)"}},
		{"N2 xml attribute reference", `request: {body: '<Credentials password="${env:PW}"/>'}`, nil},
		{"N3 duplicate json keys", `request: {body: '{"password":"hunter2","password":"${env:PW}"}'}`, []string{"request.body.password#password"}},
		{"N4 letter-only secret under *_NAME", `variables: {API_TOKEN_NAME: hunter}`, []string{"variables.API_TOKEN_NAME#API_TOKEN_NAME"}},
		{"N4 header-name metadata", `variables: {TOKEN_HEADER: X-Auth-Token, TOKEN_TYPE: Bearer, AUTH_TOKEN_HEADER: Authorization}`, nil},
		{"N5 malformed query pair", `request: {url: "https://x/?api_key=hunter2;foo=%zz"}`, []string{"request.url#api_key(text)"}},
		{"N5 malformed form pair", `request: {body: "password=hunter2&bad=%zz"}`, []string{"request.body#password(text)"}},
		{"N6 hex token without an id key", `variables: {SERVICE: 0123456789abcdef0123456789abcdef}`, []string{"variables.SERVICE#SERVICE"}},
		{"N6 single-class token", `headers: {X-Service: abcdefghijklmnopqrstuvwxyzab}`, []string{"headers.X-Service#X-Service"}},
		{"N7 compact names", `variables: {clientsecret: a1, privatekey: a2, accesskey: a3, dbpassword: a4}`,
			[]string{"variables.accesskey#accesskey", "variables.clientsecret#clientsecret", "variables.dbpassword#dbpassword", "variables.privatekey#privatekey"}},
		{"N10 relative secret file", `auth: [{config: {passwordFile: secrets/pw}}]`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ScanFile("p.yaml", []byte(tt.yaml))
			if err != nil {
				t.Fatal(err)
			}
			g, w := paths(got), tt.want
			slicesSort(g)
			slicesSort(w)
			if strings.Join(g, ",") != strings.Join(w, ",") {
				t.Fatalf("got %v, want %v", g, w)
			}
			for _, f := range got {
				if strings.Contains(f.Message(), "hunter2") {
					t.Fatalf("message echoes the value: %s", f.Message())
				}
			}
		})
	}
}

func slicesSort(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func TestSensitive(t *testing.T) {
	dup := `{"password":"hunter2","password":"${env:PW}"}`
	var collapsed map[string]any
	if err := json.Unmarshal([]byte(dup), &collapsed); err != nil {
		t.Fatal(err)
	}
	if Sensitive("password", collapsed["password"], false) {
		t.Fatal("a map round-trip keeps only the safe duplicate; that must not count as the scan")
	}
	tests := []struct {
		key   string
		value any
		place bool
		want  bool
	}{
		{"url", "https://user:hunter2@example.test/path?api_key=abc", false, true},
		{"url", "https://example.test/widgets", false, false},
		{"body", "password=hunter2&user=ada", false, true},
		{"body", "<Password>hunter2</Password>", false, true},
		{"body", `{"password":1234}`, false, true},
		{"body", dup, false, true},
		{"body", `{"name":"ada"}`, false, false},
		{"password", 1234, false, true},
		{"password", json.Number("1234"), true, true},
		{"extra", map[string]any{"password": "hunter2"}, true, true},
		{"extra", map[string]any{"name": "ada"}, true, false},
		{"note", "hunter2", false, false},
	}
	for _, tt := range tests {
		if got := Sensitive(tt.key, tt.value, tt.place); got != tt.want {
			t.Errorf("Sensitive(%q, %#v, %v) = %v, want %v", tt.key, tt.value, tt.place, got, tt.want)
		}
	}
}

func TestSensitiveYAMLAnchorCycle(t *testing.T) {
	const doc = "" +
		"auth:\n" +
		"  - name: basic\n" +
		"    type: basic\n" +
		"    config: &c\n" +
		"      name: ada\n" +
		"      items:\n" +
		"        - *c\n"
	// yaml.v3 refuses to unmarshal a self-anchor into interface{} ("anchor
	// value contains itself"), so a project load of this document fails
	// before Sensitive runs. The walker still has to survive the same
	// pointer cycle a decoder that did alias the map would hand it.
	var decoded any
	if err := yaml.Unmarshal([]byte(doc), &decoded); err == nil {
		t.Fatal("yaml.v3 accepted a self-anchor; the cycle fixture below is the one Sensitive must walk")
	}
	cfg := map[string]any{"name": "ada"}
	cfg["items"] = []any{cfg}
	items, ok := cfg["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items %#v", cfg["items"])
	}
	if reflect.ValueOf(items[0]).Pointer() != reflect.ValueOf(cfg).Pointer() {
		t.Fatal("cycle fixture did not alias the config map")
	}
	tree := map[string]any{"auth": []any{map[string]any{
		"name": "basic", "type": "basic", "config": cfg,
	}}}
	if Sensitive("", tree, false) {
		t.Fatal("the cycle fixture has no secret")
	}
}

func TestSensitiveCredentialPlaces(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abcdefghijklmnop"
	assertions := []any{
		map[string]any{
			"type": "jsonpath",
			"expr": "$.ok",
			"config": map[string]any{
				"headers": map[string]any{"X-Custom": jwt},
			},
		},
	}
	if !Sensitive("", assertions, false) {
		t.Fatal("assertion header token was not flagged")
	}
	for _, key := range []string{"query", "variables", "vars"} {
		tree := map[string]any{key: map[string]any{"X-Custom": jwt}}
		if !Sensitive("", tree, false) {
			t.Fatalf("%s token was not flagged", key)
		}
	}
	plain := map[string]any{"config": map[string]any{"note": "hello"}}
	if Sensitive("", plain, false) {
		t.Fatal("assertion-style config was flagged")
	}
	auth := []any{map[string]any{"name": "basic", "config": map[string]any{"token": jwt}}}
	if !Sensitive("auth", auth, false) {
		t.Fatal("auth config token was not flagged")
	}
}

func TestInTextTokens(t *testing.T) {
	ghp := "ghp_" + strings.Repeat("a", 36)
	// Assembled at run time so hosted push protection does not mistake the
	// fixture for a real Slack token.
	slack := "xox" + "b-123456789012-abcdefghijklmnopqrstuvwx"
	aiza := "AIza" + strings.Repeat("A", 35)
	tests := []struct {
		name, yaml string
		want       string // path#key(text) or ""
	}{
		{"aws in prose", "note: \"key AKIAIOSFODNN7EXAMPLE here\"\n", "note#note(text)"},
		{"github", "note: \"token " + ghp + " here\"\n", "note#note(text)"},
		{"github pat", "note: \"github_pat_11AAAAAAA0aaaaaaaaaaaaaaaa_bbbbbbbbbbbbbbbbbbbbbb\"\n", "note#note(text)"},
		{"stripe", "note: \"sk_live_51HhExampleSecretValue\"\n", "note#note(text)"},
		{"slack", "note: \"" + slack + "\"\n", "note#note(text)"},
		{"google", "note: \"" + aiza + "\"\n", "note#note(text)"},
		{"jwt", "note: \"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abcdefghijklmnop\"\n", "note#note(text)"},
		{"pem", "note: \"-----BEGIN RSA PRIVATE KEY-----\"\n", "note#note(text)"},
		{"mock path", "mocks:\n  - path: /api/sk_live_51HhExampleSecretValue\n", "mocks[0].path#path(text)"},
		{"header url", "headers:\n  Location: \"https://user:s3cr3t-password@example.com/cb\"\n", "headers.Location#password(text)"},
		{"ordinary", "note: \"ordinary sentence about shipping widgets\"\n", ""},
		{"uuid", "id: \"550e8400-e29b-41d4-a716-446655440000\"\n", ""},
		{"hash", "hash: \"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"\n", ""},
		{"short prefixes", "note: \"AKIA123 ghp_short sk_live_short xoxb-1\"\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ScanFile("p.yaml", []byte(tt.yaml))
			if err != nil {
				t.Fatal(err)
			}
			var hit string
			for _, f := range got {
				if f.InText {
					hit = f.PathString() + "#" + f.Key + "(text)"
					if f.Reason != "shape" && f.Key != "password" {
						t.Errorf("reason %q", f.Reason)
					}
				}
			}
			if tt.want == "" && len(got) != 0 {
				t.Fatalf("findings: %+v", got)
			}
			if tt.want != "" && hit != tt.want {
				t.Fatalf("in-text %q, findings %+v", hit, got)
			}
		})
	}
}

func TestProjectYAMLStaysClean(t *testing.T) {
	roots := []string{filepath.Join("..", "..", "examples"), filepath.Join("..", "..", "docs")}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if filepath.Base(path) != "project.yaml" && filepath.Base(path) != "project.yml" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			got, err := ScanFile(path, data)
			if err != nil {
				return err
			}
			for _, f := range got {
				t.Errorf("%s:%d %s", f.File, f.Line, f.Message())
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
}

func TestWords(t *testing.T) {
	for in, want := range map[string]string{
		"X-Api-Key": "x,api,key", "clientSecret": "client,secret", "APIKey": "api,key", "API_TOKEN_NAME": "api,token,name", "PWD": "pwd",
	} {
		if got := strings.Join(words(in), ","); got != want {
			t.Errorf("words(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAuthorizationSchemeRequiresWhitespace(t *testing.T) {
	for value, want := range map[string]string{
		"token123":                               "token123",
		"BasicABC":                               "BasicABC",
		"BearerXYZ":                              "BearerXYZ",
		"token 123":                              "123",
		"Bearer\tcredential":                     "credential",
		"Bearer ${env:TOKEN}":                    "",
		"Token ${secret:keychain:project/TOKEN}": "",
	} {
		if got := Literal("Authorization", value); got != want {
			t.Errorf("Literal(%q) = %q, want %q", value, got, want)
		}
	}
}
