package schema

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

func TestProjectSchemaIsValidJSON(t *testing.T) {
	var v any
	if err := json.Unmarshal([]byte(ProjectSchemaJSON()), &v); err != nil {
		t.Fatalf("project schema is not valid JSON: %v", err)
	}
}

func TestAuthSchemaIsValidJSON(t *testing.T) {
	var v any
	if err := json.Unmarshal([]byte(AuthSchemaJSON()), &v); err != nil {
		t.Fatalf("auth schema is not valid JSON: %v", err)
	}
}

func TestValidateProjectBytesValid(t *testing.T) {
	good := `{
      "schemaVersion": "1.0",
      "name": "x",
      "suites": [
        {"name": "s", "cases": [
          {"name": "c", "steps": [{"name": "st", "protocol": "rest"}]}
        ]}
      ]
    }`
	problems, err := ValidateProjectBytes([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("expected valid, got problems: %v", problems)
	}
}

func TestValidateProjectBytesPreservesLargeJSONNumbers(t *testing.T) {
	good := `{"schemaVersion":"1.0","name":"x","suites":[{"name":"s","cases":[{"name":"c","steps":[{"name":"st","protocol":"rest","request":{"json":{"n":1e400}}}]}]}]}`
	problems, err := ValidateProjectBytes([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("expected valid large number, got problems: %v", problems)
	}
	if _, err := ValidateProjectBytes([]byte(good + ` {}`)); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
}

func TestValidateProjectBytesRetry(t *testing.T) {
	good := `{
      "schemaVersion": "1.0",
      "name": "x",
      "suites": [
        {"name": "s", "cases": [
          {"name": "c", "steps": [{
            "name": "st",
            "protocol": "rest",
            "timeoutMs": 1500,
            "retry": {"count": 2, "delayMs": 10, "until": "status"}
          }]}
        ]}
      ]
    }`
	problems, err := ValidateProjectBytes([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("expected valid retry step, got %v", problems)
	}

	bad := `{
      "schemaVersion": "1.0",
      "name": "x",
      "suites": [
        {"name": "s", "cases": [
          {"name": "c", "steps": [{
            "name": "st",
            "protocol": "rest",
            "retry": {"count": 21, "until": "always"}
          }]}
        ]}
      ]
    }`
	problems, err = ValidateProjectBytes([]byte(bad))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) == 0 {
		t.Fatal("expected schema problems for retry count and until")
	}
}

func TestValidateProjectBytesMocks(t *testing.T) {
	good := `{
      "schemaVersion": "1.0",
      "name": "x",
      "suites": [
        {"name": "s", "cases": [
          {"name": "c", "steps": [{"name": "st", "protocol": "rest"}]}
        ]}
      ],
      "mocks": [
        {"method": "GET", "path": "/widgets/{id}", "status": 200, "json": {"id": 1},
         "match": {"query": {"active": "true"}, "bodyContains": "widget"}}
      ]
    }`
	problems, err := ValidateProjectBytes([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("mocks: %v", problems)
	}
	both := `{
      "schemaVersion": "1.0",
      "name": "x",
      "suites": [
        {"name": "s", "cases": [
          {"name": "c", "steps": [{"name": "st", "protocol": "rest"}]}
        ]}
      ],
      "mocks": [{"method": "GET", "path": "/ping", "body": "ok", "json": {"ok": true}}]
    }`
	problems, err = ValidateProjectBytes([]byte(both))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) == 0 {
		t.Fatal("body and json together must fail schema validation")
	}
}

func TestValidateProjectBytesInvalid(t *testing.T) {
	// Missing required "suites" and "name".
	bad := `{"schemaVersion": "1.0"}`
	problems, err := ValidateProjectBytes([]byte(bad))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) == 0 {
		t.Fatal("expected schema problems for invalid project")
	}
}

func TestTierTwoAuthSchema(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("auth.json", strings.NewReader(AuthSchemaJSON())); err != nil {
		t.Fatal(err)
	}
	sch, err := compiler.Compile("auth.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		config string
		valid  bool
	}{
		{"digest", `{"type":"digest","config":{"username":"user","password":"${env:PASSWORD}","algorithm":"SHA-256-sess"}}`, true},
		{"missing digest password", `{"type":"digest","config":{"username":"user"}}`, false},
		{"unsupported digest algorithm", `{"type":"digest","config":{"username":"user","password":"${env:PASSWORD}","algorithm":"unknown"}}`, false},
		{"public PKCE", `{"type":"oauth2","config":{"grant":"authorization_code","authUrl":"${AUTH_URL}","tokenUrl":"${TOKEN_URL}","clientId":"client","scopes":["read"],"redirectPort":0}}`, true},
		{"headless PKCE", `{"type":"oauth2","config":{"grant":"authorization_code","authUrl":"${AUTH_URL}","tokenUrl":"${TOKEN_URL}","clientId":"client","refreshToken":"${env:REFRESH_TOKEN}"}}`, true},
		{"missing auth URL", `{"type":"oauth2","config":{"grant":"authorization_code","tokenUrl":"${TOKEN_URL}","clientId":"client"}}`, false},
		{"bad redirect port", `{"type":"oauth2","config":{"grant":"authorization_code","authUrl":"${AUTH_URL}","tokenUrl":"${TOKEN_URL}","clientId":"client","redirectPort":-1}}`, false},
		{"confidential grant", `{"type":"oauth2","config":{"tokenUrl":"${TOKEN_URL}","clientId":"client"}}`, false},
		{"sigv4 env keys", `{"type":"aws_sigv4","config":{"region":"us-east-1","service":"execute-api"}}`, true},
		{"sigv4 explicit", `{"type":"aws_sigv4","config":{"accessKeyId":"${env:AWS_ACCESS_KEY_ID}","secretAccessKey":"${env:AWS_SECRET_ACCESS_KEY}","sessionToken":"${env:AWS_SESSION_TOKEN}","region":"us-east-1","service":"s3","unsignedPayload":true}}`, true},
		{"sigv4 missing region", `{"type":"aws_sigv4","config":{"service":"s3"}}`, false},
		{"hmac", `{"type":"hmac","config":{"secret":"${env:HMAC_SECRET}","algorithm":"sha512","encoding":"base64","prefix":"sha512=","message":"${method}\n${body}"}}`, true},
		{"hmac default body", `{"type":"hmac","config":{"secret":"${env:HMAC_SECRET}"}}`, true},
		{"hmac missing secret", `{"type":"hmac","config":{"algorithm":"sha256"}}`, false},
		{"hmac bad algorithm", `{"type":"hmac","config":{"secret":"${env:HMAC_SECRET}","algorithm":"md5"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var doc any
			if err := json.Unmarshal([]byte(tc.config), &doc); err != nil {
				t.Fatal(err)
			}
			if err := sch.Validate(doc); (err == nil) != tc.valid {
				t.Fatalf("valid=%t err=%v", tc.valid, err)
			}
		})
	}
	for name, want := range map[string]string{"auth.schema.json": AuthSchemaJSON(), "project.schema.json": ProjectSchemaJSON()} {
		got, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want+"\n" {
			t.Fatalf("published schema %s differs from source", name)
		}
	}
}
