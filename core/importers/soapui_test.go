package importers

// Independent clean-room importer. Not affiliated with or endorsed by
// SmartBear. Reads SoapUI OSS (EUPL) project files; contains no SoapUI code.

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/assertions"
	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/runner"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

func TestRewriteExpr(t *testing.T) {
	tests := []struct {
		in    string
		out   string
		warn  string
		quiet bool
	}{
		{in: "${#Project#base}", out: "${base}", quiet: true},
		{in: "prefix-${#TestSuite#host}/x", out: "prefix-${host}/x", quiet: true},
		{in: "${#TestCase#sku}", out: "${sku}", quiet: true},
		{in: "${#Global#token}", out: "${token}", warn: "global properties"},
		{in: "${#System#user}", out: "${user}", warn: "system properties"},
		{in: "${#Env#TOKEN}", out: "${TOKEN}", warn: "environment properties"},
		{in: "${#MockService#port}", out: "${port}", warn: "no Verity equivalent"},
		{in: "before ${= 'hidden-value'} after", out: "before  after", warn: "Groovy"},
		{in: `${= { true }; "s3cr3t" }`, out: "", warn: "Groovy"},
		{in: `before ${= "a}b" } after`, out: "before  after", warn: "Groovy"},
		{in: `pre ${= "a\"}" } post`, out: "pre  post", warn: "Groovy"},
		{in: `ok ${= { "s3cr3t"`, out: "ok ", warn: "Groovy"},
		{in: "${sku}", out: "${sku}", quiet: true},
		{in: "", out: "", quiet: true},
		{in: "${#Project#a}${#Env#B}", out: "${a}${B}", warn: "environment properties"},
		{in: "${#Project# name }", out: "${name}", quiet: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, warns := rewriteExpr(tt.in)
			if got != tt.out {
				t.Fatalf("rewriteExpr(%q) = %q, want %q", tt.in, got, tt.out)
			}
			blob := strings.Join(warns, "\n")
			if tt.quiet && len(warns) != 0 {
				t.Fatalf("unexpected warnings: %v", warns)
			}
			if tt.warn != "" && !strings.Contains(blob, tt.warn) {
				t.Fatalf("warnings %v do not contain %q", warns, tt.warn)
			}
			if strings.Contains(got, "hidden-value") || strings.Contains(blob, "hidden-value") ||
				strings.Contains(got, "s3cr3t") || strings.Contains(blob, "s3cr3t") ||
				strings.Contains(got, `a}b`) || strings.Contains(blob, `a}b`) {
				t.Fatal("Groovy expression leaked into the result or a warning")
			}
		})
	}
}

func TestSoapUIAssertions(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		typ   string
		op    string
		expr  string
		value any
		warn  string
	}{
		{
			name: "valid status",
			body: `<con:assertion type="Valid HTTP Status Codes" name="status"><con:configuration><codes>200</codes></con:configuration></con:assertion>`,
			typ:  "status", op: "eq", value: 200,
		},
		{
			name: "invalid status",
			body: `<con:assertion type="Invalid HTTP Status Codes" name="bad"><con:configuration><codes>500</codes></con:configuration></con:assertion>`,
			typ:  "status", op: "ne", value: 500,
		},
		{
			name: "response sla",
			body: `<con:assertion type="Response SLA" name="fast"><con:configuration><SLA>250</SLA></con:configuration></con:assertion>`,
			typ:  "response_time", op: "lte", value: 250,
		},
		{
			name: "xpath",
			body: `<con:assertion type="XPath Match" name="id"><con:configuration><path>//id</path><content>42</content></con:configuration></con:assertion>`,
			typ:  "xpath", op: "eq", expr: "//id", value: "42",
		},
		{
			name: "jsonpath",
			body: `<con:assertion type="JsonPath Match" name="name"><con:configuration><path>$.name</path><content>widget</content></con:configuration></con:assertion>`,
			typ:  "jsonpath", op: "eq", expr: "$.name", value: "widget",
		},
		{
			name: "contains",
			body: `<con:assertion type="Contains" name="has"><con:configuration><token>hello</token></con:configuration></con:assertion>`,
			typ:  "contains", value: "hello",
		},
		{
			name: "not contains",
			body: `<con:assertion type="Not Contains" name="lacks"><con:configuration><token>ERR</token></con:configuration></con:assertion>`,
			typ:  "contains", op: "not_exists", value: "ERR",
		},
		{
			name: "schema compliance",
			body: `<con:assertion type="Schema Compliance" name="schema"><con:configuration><definition>schemas/item.xsd</definition></con:configuration></con:assertion>`,
			typ:  "xsd", warn: "well-formedness",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := importXML(t, soapProject(tt.body))
			got := res.Project.Suites[0].Cases[0].Steps[0].Assertions
			if len(got) != 1 {
				t.Fatalf("got %#v warnings %v", got, res.Warnings)
			}
			a := got[0]
			if a.Type != tt.typ || a.Op != tt.op || a.Expr != tt.expr || !reflect.DeepEqual(a.Value, tt.value) {
				t.Fatalf("got type=%s op=%s expr=%q value=%#v", a.Type, a.Op, a.Expr, a.Value)
			}
			if tt.warn != "" && !hasWarn(res, tt.warn) {
				t.Fatalf("warnings: %v", res.Warnings)
			}
		})
	}
}

func TestSoapUIStatusMembership(t *testing.T) {
	res := importXML(t, soapProject(`<con:assertion type="Valid HTTP Status Codes" name="ok"><con:configuration><codes>200, 201</codes></con:configuration></con:assertion>`))
	a := res.Project.Suites[0].Cases[0].Steps[0].Assertions[0]
	if a.Type != "status" || a.Op != "matches" || a.Value != "^(200|201)$" {
		t.Fatalf("membership: %#v", a)
	}
	reg := registry.New()
	assertions.Register(reg)
	impl, err := reg.Assertion("status")
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []int{200, 201} {
		got, err := impl.Evaluate(context.Background(), &spi.AssertionContext{
			Response: &spi.Response{StatusCode: code},
			Redactor: secrets.NewRedactor(),
		}, spi.AssertionSpec{Op: a.Op, Value: a.Value})
		if err != nil || !got.Passed {
			t.Fatalf("status %d: %+v %v", code, got, err)
		}
	}
	got, err := impl.Evaluate(context.Background(), &spi.AssertionContext{
		Response: &spi.Response{StatusCode: 404},
		Redactor: secrets.NewRedactor(),
	}, spi.AssertionSpec{Op: a.Op, Value: a.Value})
	if err != nil || got.Passed {
		t.Fatalf("404 should fail membership, got %+v %v", got, err)
	}
}

func TestSoapUIInvalidStatusSet(t *testing.T) {
	res := importXML(t, soapProject(`<con:assertion type="Invalid HTTP Status Codes" name="bad"><con:configuration><codes>400,500</codes></con:configuration></con:assertion>`))
	got := res.Project.Suites[0].Cases[0].Steps[0].Assertions
	if len(got) != 2 || got[0].Op != "ne" || got[0].Value != 400 || got[1].Op != "ne" || got[1].Value != 500 {
		t.Fatalf("got %#v", got)
	}
	if !hasWarn(res, "separate status ne") {
		t.Fatalf("warnings: %v", res.Warnings)
	}
}

func TestSoapUIRESTSchemaAndRequest(t *testing.T) {
	res := importXML(t, restProject(`<con:assertion type="Schema Compliance" name="schema"><con:configuration><definition>{"type":"object"}</definition></con:configuration></con:assertion>`))
	st := res.Project.Suites[0].Cases[0].Steps[0]
	if st.Protocol != "rest" {
		t.Fatalf("protocol %s", st.Protocol)
	}
	if st.Request["method"] != "GET" || st.Request["url"] != "${ENDPOINT}/v1/things" {
		t.Fatalf("request %#v env %#v", st.Request, res.Project.Environments[0].Variables)
	}
	if len(st.Assertions) != 1 || st.Assertions[0].Type != "json_schema" {
		t.Fatalf("assertions %#v", st.Assertions)
	}
	if st.Assertions[0].Config["schema"] != `{"type":"object"}` {
		t.Fatalf("schema %#v", st.Assertions[0].Config)
	}

	pathRes := importXML(t, restProject(`<con:assertion type="Schema Compliance" name="schema"><con:configuration><definition>schemas/item.json</definition></con:configuration></con:assertion>`))
	a := pathRes.Project.Suites[0].Cases[0].Steps[0].Assertions[0]
	if a.Type != "json_schema" || a.Expr != "schemas/item.json" {
		t.Fatalf("file schema %#v", a)
	}
	if !hasWarn(pathRes, "not copied") || !hasWarn(pathRes, "generated project") {
		t.Fatalf("warnings: %v", pathRes.Warnings)
	}

	empty := importXML(t, restProject(`<con:assertion type="Script Assertion" name="custom"><con:configuration/></con:assertion>`))
	step := empty.Project.Suites[0].Cases[0].Steps[0]
	if len(step.Assertions) != 0 || step.Skip {
		t.Fatalf("step %+v", step)
	}
	if !hasWarn(empty, "not converted") || !hasWarn(empty, "step kept") {
		t.Fatalf("warnings: %v", empty.Warnings)
	}
}

func TestSoapUIEndpointsTransfersAndProperties(t *testing.T) {
	res := importXML(t, `<?xml version="1.0" encoding="UTF-8"?>
<con:soapui-project name="Endpoints" xmlns:con="http://eviware.com/soapui/config">
  <con:interface name="S" type="wsdl" soapVersion="1_2">
    <con:endpoints>
      <con:endpoint>svc-a</con:endpoint>
      <con:endpoint>svc-b</con:endpoint>
    </con:endpoints>
    <con:operation name="Op" action="urn:op"/>
  </con:interface>
  <con:testSuite name="Suite">
    <con:properties>
      <con:property><con:name>suiteNote</con:name><con:value>from-suite</con:value></con:property>
    </con:properties>
    <con:testCase name="Case">
      <con:testStep type="properties" name="Init">
        <con:config>
          <con:properties>
            <con:property><con:name>sku</con:name><con:value>abc</con:value></con:property>
          </con:properties>
        </con:config>
      </con:testStep>
      <con:testStep type="request" name="Call">
        <con:config>
          <con:interface>S</con:interface>
          <con:operation>Op</con:operation>
          <con:request name="Op">
            <con:endpoint>svc-b</con:endpoint>
            <con:request/>
          </con:request>
        </con:config>
      </con:testStep>
      <con:testStep type="transfer" name="Copy">
        <con:config>
          <con:transfers>
            <con:name>Copy</con:name>
            <con:sourceType>Response</con:sourceType>
            <con:sourceStep>Call</con:sourceStep>
            <con:sourcePath>//id</con:sourcePath>
            <con:targetType>itemId</con:targetType>
            <con:targetStep>#TestCase#</con:targetStep>
          </con:transfers>
        </con:config>
      </con:testStep>
      <con:testStep type="request" name="Second" disabled="true">
        <con:config>
          <con:interface>S</con:interface>
          <con:operation>Op</con:operation>
          <con:request name="Op">
            <con:endpoint>svc-a</con:endpoint>
            <con:request/>
          </con:request>
        </con:config>
      </con:testStep>
    </con:testCase>
  </con:testSuite>
</con:soapui-project>`)
	env := res.Project.Environments[0].Variables
	if env["ENDPOINT"] != "svc-a" || env["ENDPOINT_1"] != "svc-b" {
		t.Fatalf("endpoints %#v", env)
	}
	if res.Project.Suites[0].Vars["suiteNote"] != "from-suite" {
		t.Fatalf("suite vars %#v", res.Project.Suites[0].Vars)
	}
	steps := res.Project.Suites[0].Cases[0].Steps
	if len(steps) != 2 {
		t.Fatalf("steps %d", len(steps))
	}
	call := steps[0]
	if call.Protocol != "soap" || call.Request["url"] != "${ENDPOINT_1}" || call.Request["version"] != "1.2" || call.Request["soapAction"] != "urn:op" {
		t.Fatalf("soap request %#v", call.Request)
	}
	if call.Vars["sku"] != "abc" {
		t.Fatalf("properties step vars %#v", call.Vars)
	}
	if len(call.Transfers) != 1 || call.Transfers[0].Name != "itemId" || call.Transfers[0].From != "xpath" || call.Transfers[0].Expr != "//id" {
		t.Fatalf("transfers %#v", call.Transfers)
	}
	if !steps[1].Skip || steps[1].Request["url"] != "${ENDPOINT}" {
		t.Fatalf("disabled step %#v", steps[1])
	}
	if !hasWarn(res, "disabled in SoapUI") {
		t.Fatalf("warnings %v", res.Warnings)
	}
}

func TestSoapUISecretsAndUnsupported(t *testing.T) {
	const password = "not-a-real-password"
	const apiKey = "abcdefghijklmnopqrstuvwx"
	res := importXML(t, `<?xml version="1.0" encoding="UTF-8"?>
<con:soapui-project name="Secrets" xmlns:con="http://eviware.com/soapui/config">
  <con:interface name="S" type="wsdl" soapVersion="1_1">
    <con:endpoints><con:endpoint>svc-a</con:endpoint></con:endpoints>
    <con:operation name="Op" action="urn:op"/>
  </con:interface>
  <con:testSuite name="Suite">
    <con:testCase name="Case">
      <con:testStep type="groovy" name="Init"><con:config><script>log.info("skip")</script></con:config></con:testStep>
      <con:testStep type="jdbc" name="Query"><con:config/></con:testStep>
      <con:testStep type="request" name="Call">
        <con:config>
          <con:interface>S</con:interface>
          <con:operation>Op</con:operation>
          <con:request name="Op">
            <con:endpoint>svc-a</con:endpoint>
            <con:request>{"password":"`+password+`"}</con:request>
            <con:settings>
              <con:setting id="request@request-headers">&lt;xml-fragment&gt;&lt;con:entry key="Accept" value="application/json"/&gt;&lt;con:entry key="X-Api-Key" value="`+apiKey+`"/&gt;&lt;/xml-fragment&gt;</con:setting>
            </con:settings>
            <con:credentials>
              <con:authType>Preemptive</con:authType>
              <con:username>demo</con:username>
              <con:password>`+password+`</con:password>
            </con:credentials>
            <con:assertion type="Script Assertion" name="custom"><con:configuration/></con:assertion>
          </con:request>
        </con:config>
      </con:testStep>
      <con:loadTest name="Busy"/>
      <con:securityTest name="Scan"/>
    </con:testCase>
  </con:testSuite>
  <con:mockService name="SampleMock"/>
  <con:oAuth2ProfileContainer><con:oAuth2Profile name="O"/></con:oAuth2ProfileContainer>
  <con:properties>
    <con:property><con:name>password</con:name><con:value>`+password+`</con:value></con:property>
    <con:property><con:name>area</con:name><con:value>${#Global#region}</con:value></con:property>
  </con:properties>
</con:soapui-project>`)
	assertNoSecret(t, res, password)
	assertNoSecret(t, res, apiKey)
	env := res.Project.Environments[0].Variables
	if env["password"] != "${env:PASSWORD}" || env["area"] != "${region}" {
		t.Fatalf("vars %#v", env)
	}
	step := res.Project.Suites[0].Cases[0].Steps[0]
	if step.Name != "Call" || step.Auth == nil || len(step.Auth.Use) != 1 {
		t.Fatalf("step %#v", step)
	}
	auth := res.Project.Auth[0]
	if auth.Type != "basic" || auth.Config["username"] != "demo" || auth.Config["password"] != "${env:PASSWORD}" {
		t.Fatalf("auth %#v", auth)
	}
	headers, _ := step.Request["headers"].(map[string]any)
	if headers["Accept"] != "application/json" || headers["X-Api-Key"] != "${env:X_API_KEY}" {
		t.Fatalf("headers %#v", headers)
	}
	body, _ := step.Request["body"].(string)
	if strings.Contains(body, password) || !strings.Contains(body, "${env:PASSWORD}") {
		t.Fatalf("body %q", body)
	}
	for _, want := range []string{
		"Groovy/script step not converted",
		"JDBC/DataSource step not converted",
		"mock service",
		"load test",
		"security test",
		"OAuth 2 profile",
		"Script Assertion",
		"step kept",
		"global properties",
		"PASSWORD",
	} {
		if !hasWarn(res, want) {
			t.Fatalf("missing %q in %v", want, res.Warnings)
		}
	}
}

func TestSoapUIGlobalHTTPAuthDropsPassword(t *testing.T) {
	const password = "not-a-real-password"
	res := importXML(t, soapProject(`
            <con:credentials>
              <con:authType>Global HTTP Settings</con:authType>
              <con:username>demo</con:username>
              <con:password>`+password+`</con:password>
            </con:credentials>`))
	assertNoSecret(t, res, password)
	if len(res.Project.Auth) != 0 {
		t.Fatalf("auth %#v", res.Project.Auth)
	}
	if !hasWarn(res, "Global HTTP Settings") {
		t.Fatalf("warnings %v", res.Warnings)
	}
}

func TestImportSoapUIRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"garbage", "<not-xml", "parse SoapUI"},
		{"wrong root", `<other xmlns="http://eviware.com/soapui/config"/>`, "root element"},
		{"namespace", `<soapui-project name="X"/>`, "namespace"},
		{"no requests", `<?xml version="1.0" encoding="UTF-8"?>
<con:soapui-project name="Empty" xmlns:con="http://eviware.com/soapui/config">
  <con:testSuite name="Suite"><con:testCase name="Case">
    <con:testStep type="groovy" name="Only"><con:config/></con:testStep>
  </con:testCase></con:testSuite>
</con:soapui-project>`, "no SOAP or REST"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.xml")
			if err := os.WriteFile(path, []byte(tt.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := ImportSoapUI(path)
			ve, ok := verr.As(err)
			if !ok || ve.Code != verr.CodeImport || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
	_, err := ImportSoapUI(filepath.Join(t.TempDir(), "missing.xml"))
	if ve, ok := verr.As(err); !ok || ve.Code != verr.CodeImport {
		t.Fatalf("missing file: %v", err)
	}
}

func TestImportSampleSoapUI(t *testing.T) {
	res, err := ImportSoapUI(filepath.Join("..", "..", "examples", "specs", "sample.soapui.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if errs := res.Project.Validate(); len(errs) != 0 {
		t.Fatalf("validate: %v", errs)
	}
	p := res.Project
	if p.Name != "Sample API" || len(p.Suites) != 1 || p.Suites[0].Name != "Smoke" {
		t.Fatalf("project shape %+v", p.Suites)
	}
	c := p.Suites[0].Cases[0]
	if c.Name != "Lookup" || c.Vars["sku"] != "abc" || len(c.Steps) != 2 {
		t.Fatalf("case %#v", c)
	}
	soap := c.Steps[0]
	if soap.Name != "Get item" || soap.Protocol != "soap" {
		t.Fatalf("soap step %+v", soap)
	}
	if soap.Request["url"] != "${ENDPOINT}/soap" || soap.Request["soapAction"] != "urn:sample:getItem" || soap.Request["version"] != "1.1" {
		t.Fatalf("soap request %#v", soap.Request)
	}
	body, _ := soap.Request["body"].(string)
	if !strings.Contains(body, "<sku>${sku}</sku>") {
		t.Fatalf("body %s", body)
	}
	if len(soap.Transfers) != 1 || soap.Transfers[0].Name != "itemId" || soap.Transfers[0].From != "xpath" {
		t.Fatalf("transfers %#v", soap.Transfers)
	}
	rest := c.Steps[1]
	if rest.Protocol != "rest" || rest.Request["method"] != "GET" || rest.Request["url"] != "${ENDPOINT}/api/items/${itemId}" {
		t.Fatalf("rest %#v", rest.Request)
	}
	if p.Environments[0].Variables["password"] != "${env:PASSWORD}" {
		t.Fatalf("env %#v", p.Environments[0].Variables)
	}
	assertNoSecret(t, res, "not-a-real-password")
	for _, want := range []string{"Groovy/script", "mock service", "load test", "security test", "PASSWORD"} {
		if !hasWarn(res, want) {
			t.Fatalf("missing %q in %v", want, res.Warnings)
		}
	}
}

func TestImportedSoapUIRuns(t *testing.T) {
	res, err := ImportSoapUI(filepath.Join("..", "..", "examples", "specs", "sample.soapui.xml"))
	if err != nil {
		t.Fatal(err)
	}
	var sawSOAP, sawREST bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/soap":
			sawSOAP = true
			if r.Method != http.MethodPost {
				t.Errorf("soap method %s", r.Method)
			}
			if got := r.Header.Get("SOAPAction"); got != `"urn:sample:getItem"` {
				t.Errorf("SOAPAction %q", got)
			}
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), "<sku>abc</sku>") {
				t.Errorf("soap body %s", b)
			}
			w.Header().Set("Content-Type", "text/xml")
			_, _ = io.WriteString(w, `<soap:Envelope xmlns:soap="urn:envelope"><soap:Body><result>ok</result><id>42</id></soap:Body></soap:Envelope>`)
		case "/api/items/42":
			sawREST = true
			if r.Method != http.MethodGet {
				t.Errorf("rest method %s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"name":"widget","id":"42"}`)
		default:
			http.NotFound(w, r)
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	res.Project.Environments[0].Variables["ENDPOINT"] = srv.URL
	got, err := runner.New(bootstrap.Default()).Run(context.Background(), res.Project, runner.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !sawSOAP || !sawREST {
		t.Fatalf("server saw soap=%v rest=%v", sawSOAP, sawREST)
	}
	if got.Summary.Status != spi.StatusPassed {
		var b strings.Builder
		for _, su := range got.Suites {
			for _, c := range su.Cases {
				for _, st := range c.Steps {
					b.WriteString(st.Name + " " + string(st.Status))
					if st.Error != nil {
						b.WriteString(" err=" + st.Error.Message)
					}
					b.WriteByte('\n')
					for _, a := range st.Assertions {
						if !a.Passed {
							b.WriteString("  " + a.Type + " " + a.Message + "\n")
						}
					}
				}
			}
		}
		t.Fatal(b.String())
	}
}

func TestSoapUICharacterData(t *testing.T) {
	res := importXML(t, `<?xml version="1.0" encoding="UTF-8"?>
<con:soapui-project name="Chars" xmlns:con="http://eviware.com/soapui/config">
  <con:interface name="R" type="rest" basePath="/v1">
    <con:endpoints><con:endpoint>svc-a</con:endpoint></con:endpoints>
    <con:resource name="Things" path="/things">
      <con:method name="List" method="GET"/>
    </con:resource>
  </con:interface>
  <con:properties>
    <con:property><con:name>note</con:name><con:value>A&amp;B</con:value></con:property>
    <con:property><con:name>label</con:name><con:value><![CDATA[widget]]></con:value></con:property>
    <con:property><con:name>amp</con:name><con:value>&#38;</con:value></con:property>
  </con:properties>
  <con:testSuite name="Suite">
    <con:testCase name="Case">
      <con:testStep type="restrequest" name="List">
        <con:config service="R" methodName="List" resourcePath="/things">
          <con:restRequest name="List" mediaType="application/json">
            <con:endpoint>svc-a</con:endpoint>
            <con:request/>
            <con:parameters>
              <con:parameter><con:name>q</con:name><con:value>A&amp;B</con:value><con:style>QUERY</con:style></con:parameter>
              <con:parameter><con:name>label</con:name><con:value><![CDATA[widget]]></con:value><con:style>QUERY</con:style></con:parameter>
            </con:parameters>
            <con:assertion type="Valid HTTP Status Codes" name="status"><con:configuration><codes>&#50;00</codes></con:configuration></con:assertion>
            <con:assertion type="Response SLA" name="fast"><con:configuration><SLA>&#53;000</SLA></con:configuration></con:assertion>
            <con:assertion type="XPath Match" name="id"><con:configuration><path>//item&#47;id</path><content>A&amp;B</content></con:configuration></con:assertion>
            <con:assertion type="Contains" name="has"><con:configuration><token>A&#38;B</token><ignoreCase>&#116;rue</ignoreCase></con:configuration></con:assertion>
            <con:assertion type="JsonPath Match" name="name"><con:configuration><path>$.name</path><content><id>A&amp;B</id></content></con:configuration></con:assertion>
          </con:restRequest>
        </con:config>
      </con:testStep>
    </con:testCase>
  </con:testSuite>
</con:soapui-project>`)
	env := res.Project.Environments[0].Variables
	if env["note"] != "A&B" || env["label"] != "widget" || env["amp"] != "&" {
		t.Fatalf("properties %#v", env)
	}
	st := res.Project.Suites[0].Cases[0].Steps[0]
	q, _ := st.Request["query"].(map[string]any)
	if q["q"] != "A&B" || q["label"] != "widget" {
		t.Fatalf("query %#v", q)
	}
	byType := map[string]modelAssertion{}
	for _, a := range st.Assertions {
		byType[a.Type] = modelAssertion{op: a.Op, expr: a.Expr, value: a.Value}
	}
	if byType["status"].value != 200 {
		t.Fatalf("status %#v", byType["status"])
	}
	if byType["response_time"].value != 5000 {
		t.Fatalf("sla %#v", byType["response_time"])
	}
	if byType["xpath"].expr != "//item/id" || byType["xpath"].value != "A&B" {
		t.Fatalf("xpath %#v", byType["xpath"])
	}
	if byType["contains"].value != "A&B" {
		t.Fatalf("contains %#v", byType["contains"])
	}
	if byType["jsonpath"].value != "<id>A&amp;B</id>" {
		t.Fatalf("nested content %#v", byType["jsonpath"])
	}
	if !hasWarn(res, "ignoreCase") {
		t.Fatalf("decoded ignoreCase flag was not applied: %v", res.Warnings)
	}
}

type modelAssertion struct {
	op, expr string
	value    any
}

func TestSoapUISecretEdges(t *testing.T) {
	const (
		mixed    = "pre-not-a-real-password"
		password = "not-a-real-password"
		apiKey   = "abcdefghijklmnopqrstuvwx"
		token    = "token-value-one"
		groovy   = "s3cr3t-groovy"
		expect   = "ZYXWVUTSRQPONMLKJIHGFEDC"
	)
	res := importXML(t, `<?xml version="1.0" encoding="UTF-8"?>
<con:soapui-project name="Edges" xmlns:con="http://eviware.com/soapui/config">
  <con:properties>
    <con:property><con:name>password</con:name><con:value>${#Project#pw}</con:value></con:property>
  </con:properties>
  <con:interface name="S" type="wsdl" soapVersion="1_1">
    <con:endpoints><con:endpoint>svc-a</con:endpoint></con:endpoints>
    <con:operation name="Op" action="urn:op"/>
  </con:interface>
  <con:testSuite name="Suite">
    <con:testCase name="Case">
      <con:testStep type="request" name="Call">
        <con:config>
          <con:interface>S</con:interface>
          <con:operation>Op</con:operation>
          <con:request name="Op">
            <con:endpoint>https://demo:`+password+`@svc-a/service?api_key=`+apiKey+`&amp;q=hello</con:endpoint>
            <con:request>{"refresh_token":"$`+password+`"}&lt;access_token&gt;`+token+`&lt;/access_token&gt;&lt;password&gt;$`+password+`&lt;/password&gt;&lt;secret&gt;&lt;![CDATA[{`+password+`}]]&gt;&lt;/secret&gt;&lt;login password="`+password+`"/&gt;${= { true }; "`+groovy+`" }</con:request>
            <con:settings>
              <con:setting id="request@request-headers">&lt;xml-fragment&gt;&lt;con:entry key="X-Api-Key" value="`+mixed+`-${#Project#suffix}"/&gt;&lt;/xml-fragment&gt;</con:setting>
            </con:settings>
            <con:credentials>
              <con:authType>Basic</con:authType>
              <con:username>demo</con:username>
              <con:password>`+mixed+`-${#Project#suffix}</con:password>
            </con:credentials>
            <con:assertion type="Contains" name="tok"><con:configuration><token>`+expect+`</token></con:configuration></con:assertion>
            <con:assertion type="XPath Match" name="xp"><con:configuration><path>//id</path><content>`+expect+`</content></con:configuration></con:assertion>
          </con:request>
        </con:config>
      </con:testStep>
    </con:testCase>
  </con:testSuite>
</con:soapui-project>`)
	for _, secret := range []string{mixed, password, apiKey, token, groovy, expect, "$" + password, "{" + password + "}"} {
		assertNoSecret(t, res, secret)
	}
	env := res.Project.Environments[0].Variables
	if env["password"] != "${pw}" {
		t.Fatalf("template property %#v", env["password"])
	}
	auth := res.Project.Auth[0]
	pass, _ := auth.Config["password"].(string)
	if auth.Config["username"] != "demo" || !strings.HasPrefix(pass, "${env:") || strings.Contains(pass, mixed) {
		t.Fatalf("auth %#v", auth.Config)
	}
	step := res.Project.Suites[0].Cases[0].Steps[0]
	url, _ := step.Request["url"].(string)
	if !strings.Contains(url, "${env:ENDPOINT_USER}") || !strings.Contains(url, "${env:ENDPOINT_PASSWORD}") || !strings.Contains(url, "api_key=${env:API_KEY}") || !strings.Contains(url, "q=hello") {
		t.Fatalf("url %q", url)
	}
	if strings.Contains(url, "demo") || strings.Contains(url, password) {
		t.Fatalf("url kept user info %q", url)
	}
	headers, _ := step.Request["headers"].(map[string]any)
	hv, _ := headers["X-Api-Key"].(string)
	if !strings.HasPrefix(hv, "${env:") || strings.Contains(hv, mixed) {
		t.Fatalf("header %#v", headers)
	}
	body, _ := step.Request["body"].(string)
	for _, want := range []string{"${env:REFRESH_TOKEN}", "${env:ACCESS_TOKEN}", "${env:PASSWORD}", "${env:SECRET}"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body %q missing %s", body, want)
		}
	}
	if strings.Contains(body, "<![CDATA[") || strings.Contains(body, groovy) {
		t.Fatalf("body kept script or cdata %q", body)
	}
	if step.Assertions[0].Value != "${env:ASSERTION}" || step.Assertions[1].Value != "${env:ASSERTION}" {
		t.Fatalf("assertions %#v", step.Assertions)
	}
	for _, want := range []string{"literal password", "endpoint username", "endpoint password", "endpoint query", "inline Groovy", "assertion expected value"} {
		if !hasWarn(res, want) {
			t.Fatalf("missing %q in %v", want, res.Warnings)
		}
	}
}

func TestSoapUITransferOrder(t *testing.T) {
	res := importXML(t, `<?xml version="1.0" encoding="UTF-8"?>
<con:soapui-project name="Order" xmlns:con="http://eviware.com/soapui/config">
  <con:testSuite name="Suite">
    <con:testCase name="Order">
      <con:testStep type="properties" name="Before">
        <con:config><con:properties>
          <con:property><con:name>early</con:name><con:value>from-before</con:value></con:property>
        </con:properties></con:config>
      </con:testStep>
      <con:testStep type="restrequest" name="A">`+orderRequest("A", "/a", "early", "xid", "late")+`</con:testStep>
      <con:testStep type="restrequest" name="B">`+orderRequest("B", "/b", "early", "xid", "late")+`</con:testStep>
      <con:testStep type="transfer" name="Move">
        <con:config><con:transfers>
          <con:name>Move</con:name>
          <con:sourceType>Response</con:sourceType>
          <con:sourceStep>A</con:sourceStep>
          <con:sourcePath></con:sourcePath>
          <con:targetType>xid</con:targetType>
          <con:targetStep>#TestCase#</con:targetStep>
        </con:transfers></con:config>
      </con:testStep>
      <con:testStep type="restrequest" name="C">`+orderRequest("C", "/c", "early", "xid", "late")+`</con:testStep>
      <con:testStep type="properties" name="After">
        <con:config><con:properties>
          <con:property><con:name>late</con:name><con:value>from-after</con:value></con:property>
        </con:properties></con:config>
      </con:testStep>
    </con:testCase>
    <con:testCase name="Faithful">
      <con:testStep type="restrequest" name="A2">`+orderRequest("A2", "/a2", "xid2")+`</con:testStep>
      <con:testStep type="transfer" name="Move2">
        <con:config><con:transfers>
          <con:name>Move2</con:name>
          <con:sourceType>Response</con:sourceType>
          <con:sourceStep>A2</con:sourceStep>
          <con:targetType>xid2</con:targetType>
          <con:targetStep>#TestCase#</con:targetStep>
        </con:transfers></con:config>
      </con:testStep>
      <con:testStep type="restrequest" name="B2">`+orderRequest("B2", "/b2", "xid2")+`</con:testStep>
    </con:testCase>
  </con:testSuite>
</con:soapui-project>`)
	order := res.Project.Suites[0].Cases[0]
	if order.Steps[0].Vars["early"] != "from-before" {
		t.Fatalf("early vars %#v", order.Steps[0].Vars)
	}
	if len(order.Steps[0].Transfers) != 1 || order.Steps[0].Transfers[0].Name != "xid" {
		t.Fatalf("transfer attached %#v", order.Steps[0].Transfers)
	}
	if order.Vars["late"] != "from-after" {
		t.Fatalf("late case vars %#v", order.Vars)
	}
	if !hasWarn(res, `so B sees xid before the transfer step`) {
		t.Fatalf("warnings %v", res.Warnings)
	}
	if !hasWarn(res, "properties step After after the last request") || !hasWarn(res, "A, B, C will see them") {
		t.Fatalf("warnings %v", res.Warnings)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "B2 sees") {
			t.Fatalf("faithful transfer warned: %s", w)
		}
	}

	seen := map[string]map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := map[string]string{}
		for k, vs := range r.URL.Query() {
			if len(vs) > 0 {
				q[k] = vs[0]
			}
		}
		seen[r.URL.Path] = q
		body := "IDVALUE"
		if strings.HasSuffix(r.URL.Path, "2") {
			body = "IDVALUE2"
		}
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	res.Project.Environments[0].Variables["ENDPOINT"] = srv.URL
	got, err := runner.New(bootstrap.Default()).Run(context.Background(), res.Project, runner.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.Status != spi.StatusPassed {
		t.Fatalf("run status %s", got.Summary.Status)
	}
	if seen["/a"]["early"] != "from-before" || seen["/a"]["xid"] != "${xid}" || seen["/a"]["late"] != "from-after" {
		t.Fatalf("A saw %#v", seen["/a"])
	}
	if seen["/b"]["early"] != "from-before" || seen["/b"]["xid"] != "IDVALUE" || seen["/b"]["late"] != "from-after" {
		t.Fatalf("B saw %#v", seen["/b"])
	}
	if seen["/c"]["xid"] != "IDVALUE" || seen["/c"]["late"] != "from-after" {
		t.Fatalf("C saw %#v", seen["/c"])
	}
	if seen["/a2"]["xid2"] != "${xid2}" {
		t.Fatalf("A2 saw %#v", seen["/a2"])
	}
	if seen["/b2"]["xid2"] != "IDVALUE2" {
		t.Fatalf("B2 saw %#v", seen["/b2"])
	}
}

func orderRequest(name, path string, params ...string) string {
	var b strings.Builder
	b.WriteString(`<con:config methodName="GET" resourcePath="` + path + `"><con:restRequest name="` + name + `" method="GET"><con:endpoint>svc-a</con:endpoint><con:request/><con:parameters>`)
	for _, p := range params {
		b.WriteString(`<con:parameter><con:name>` + p + `</con:name><con:value>${` + p + `}</con:value><con:style>QUERY</con:style></con:parameter>`)
	}
	b.WriteString(`</con:parameters></con:restRequest></con:config>`)
	return b.String()
}

func TestSoapUIInheritedRESTParams(t *testing.T) {
	const apiKey = "abcdefghijklmnopqrstuvwx"
	res := importXML(t, `<?xml version="1.0" encoding="UTF-8"?>
<con:soapui-project name="Inherit" xmlns:con="http://eviware.com/soapui/config">
  <con:interface name="R" type="rest" basePath="/v1">
    <con:endpoints><con:endpoint>svc-a</con:endpoint></con:endpoints>
    <con:parameters>
      <con:parameter><con:name>verbose</con:name><con:value>yes</con:value><con:style>QUERY</con:style></con:parameter>
    </con:parameters>
    <con:resource name="Things" path="/things/{id}">
      <con:parameters>
        <con:parameter><con:name>api_key</con:name><con:value>`+apiKey+`</con:value><con:style>QUERY</con:style></con:parameter>
        <con:parameter><con:name>id</con:name><con:value>from-resource</con:value><con:style>TEMPLATE</con:style></con:parameter>
      </con:parameters>
      <con:method name="Get" method="GET">
        <con:parameters>
          <con:parameter><con:name>id</con:name><con:value>from-method</con:value><con:style>TEMPLATE</con:style></con:parameter>
          <con:parameter><con:name>limit</con:name><con:value>10</con:value><con:style>QUERY</con:style></con:parameter>
          <con:parameter><con:name>page</con:name><con:value></con:value><con:default>3</con:default><con:style>QUERY</con:style></con:parameter>
          <con:parameter><con:name>size</con:name><con:value></con:value><con:default>9</con:default><con:style>QUERY</con:style></con:parameter>
          <con:parameter><con:name>matrix</con:name><con:value>1</con:value><con:style>MATRIX</con:style></con:parameter>
        </con:parameters>
      </con:method>
    </con:resource>
  </con:interface>
  <con:testSuite name="Suite">
    <con:testCase name="Case">
      <con:testStep type="restrequest" name="Get">
        <con:config service="R" methodName="Get" resourcePath="/things/{id}">
          <con:restRequest name="Get" method="GET">
            <con:endpoint>svc-a</con:endpoint>
            <con:request/>
            <con:parameters>
              <con:parameter><con:name>id</con:name><con:value>from-request</con:value><con:style>TEMPLATE</con:style></con:parameter>
              <con:parameter><con:name>size</con:name><con:value></con:value><con:style>QUERY</con:style></con:parameter>
            </con:parameters>
          </con:restRequest>
        </con:config>
      </con:testStep>
      <con:testStep type="restrequest" name="Missing">
        <con:config service="R" methodName="Missing" resourcePath="/nope">
          <con:restRequest name="Missing" method="GET">
            <con:endpoint>svc-a</con:endpoint>
            <con:request/>
          </con:restRequest>
        </con:config>
      </con:testStep>
    </con:testCase>
  </con:testSuite>
</con:soapui-project>`)
	assertNoSecret(t, res, apiKey)
	st := res.Project.Suites[0].Cases[0].Steps[0]
	if st.Request["url"] != "${ENDPOINT}/v1/things/from-request" {
		t.Fatalf("url %#v", st.Request["url"])
	}
	q, _ := st.Request["query"].(map[string]any)
	if q["verbose"] != "yes" || q["limit"] != "10" || q["page"] != "3" || q["api_key"] != "${env:API_KEY}" || q["size"] != "" {
		t.Fatalf("query %#v", q)
	}
	if _, ok := q["id"]; ok {
		t.Fatalf("template id leaked into query %#v", q)
	}
	if !hasWarn(res, `matrix parameter "matrix"`) {
		t.Fatalf("warnings %v", res.Warnings)
	}
	if !hasWarn(res, "were not applied") {
		t.Fatalf("warnings %v", res.Warnings)
	}
}

func TestSoapUIIgnoredConstructs(t *testing.T) {
	const (
		script     = "doNotCopyThisScript"
		setting    = "doNotCopyThisSetting"
		attachment = "doNotCopyThisAttachment"
		unnamed    = "doNotCopyThisUnnamed"
	)
	res := importXML(t, `<?xml version="1.0" encoding="UTF-8"?>
<con:soapui-project name="Ignored" xmlns:con="http://eviware.com/soapui/config">
  <con:setupScript>`+script+`-project-setup</con:setupScript>
  <con:tearDownScript>`+script+`-project-teardown</con:tearDownScript>
  <con:settings><con:setting id="proxy">`+setting+`</con:setting></con:settings>
  <con:properties>
    <con:property><con:name> </con:name><con:value>`+unnamed+`-prop</con:value></con:property>
  </con:properties>
  <con:interface name="S" type="wsdl" soapVersion="1_1">
    <con:endpoints><con:endpoint>svc-a</con:endpoint></con:endpoints>
    <con:operation name="Op" action="urn:op"/>
  </con:interface>
  <con:testSuite name="Smoke">
    <con:setupScript>`+script+`-suite-setup</con:setupScript>
    <con:tearDownScript>`+script+`-suite-teardown</con:tearDownScript>
    <con:testCase name="Lookup">
      <con:setupScript>`+script+`-case-setup</con:setupScript>
      <con:tearDownScript>`+script+`-case-teardown</con:tearDownScript>
      <con:testStep type="restrequest" name="Call">
        <con:config methodName="GET" resourcePath="/items">
          <con:restRequest name="Call" method="GET">
            <con:endpoint>svc-a</con:endpoint>
            <con:request/>
            <con:attachment><con:name>`+attachment+`</con:name><con:data>`+attachment+`</con:data></con:attachment>
            <con:settings>
              <con:setting id="request@request-headers">&lt;xml-fragment&gt;&lt;con:entry key="Accept" value="text/plain"/&gt;&lt;/xml-fragment&gt;</con:setting>
              <con:setting id="timeout">`+setting+`-step</con:setting>
            </con:settings>
            <con:parameters>
              <con:parameter><con:name> </con:name><con:value>`+unnamed+`-param</con:value><con:style>QUERY</con:style></con:parameter>
            </con:parameters>
          </con:restRequest>
        </con:config>
      </con:testStep>
    </con:testCase>
  </con:testSuite>
</con:soapui-project>`)
	for _, secret := range []string{script, setting, attachment, unnamed} {
		assertNoSecret(t, res, secret)
	}
	for _, want := range []string{
		"project: setup script not converted",
		"project: teardown script not converted",
		"project: settings not converted",
		"project: property with no name not imported",
		"Smoke: setup script not converted",
		"Smoke: teardown script not converted",
		"Smoke/Lookup: setup script not converted",
		"Smoke/Lookup: teardown script not converted",
		"Smoke/Lookup/Call: attachments not imported",
		"Smoke/Lookup/Call: request settings other than headers not imported",
		"Smoke/Lookup/Call: parameter with no name not imported",
	} {
		if !hasWarn(res, want) {
			t.Fatalf("missing %q in %v", want, res.Warnings)
		}
	}
	headers, _ := res.Project.Suites[0].Cases[0].Steps[0].Request["headers"].(map[string]any)
	if headers["Accept"] != "text/plain" {
		t.Fatalf("headers %#v", headers)
	}
}

func TestSoapUISizeLimit(t *testing.T) {
	if soapuiMaxBytes != 50<<20 {
		t.Fatalf("limit = %d, want 50 MB", soapuiMaxBytes)
	}
	old := soapuiMaxBytes
	soapuiMaxBytes = 64
	t.Cleanup(func() { soapuiMaxBytes = old })
	path := filepath.Join(t.TempDir(), "big.xml")
	if err := os.WriteFile(path, bytes.Repeat([]byte("a"), 65), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ImportSoapUI(path)
	ve, ok := verr.As(err)
	if !ok || ve.Code != verr.CodeImport || !strings.Contains(err.Error(), "over the 64 byte limit") {
		t.Fatalf("got %v", err)
	}
}

func importXML(t *testing.T, body string) *Result {
	t.Helper()
	path := filepath.Join(t.TempDir(), "project.soapui.xml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ImportSoapUI(path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if errs := res.Project.Validate(); len(errs) != 0 {
		t.Fatalf("validate: %v", errs)
	}
	return res
}

func soapProject(assertion string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<con:soapui-project name="Fixture" xmlns:con="http://eviware.com/soapui/config">
  <con:interface name="S" type="wsdl" soapVersion="1_1">
    <con:endpoints><con:endpoint>svc-a</con:endpoint></con:endpoints>
    <con:operation name="Op" action="urn:op"/>
  </con:interface>
  <con:settings/>
  <con:testSuite name="Suite">
    <con:testCase name="Case">
      <con:testStep type="request" name="Call">
        <con:config>
          <con:interface>S</con:interface>
          <con:operation>Op</con:operation>
          <con:request name="Op">
            <con:endpoint>svc-a</con:endpoint>
            <con:request/>
            ` + assertion + `
          </con:request>
        </con:config>
      </con:testStep>
    </con:testCase>
  </con:testSuite>
</con:soapui-project>`
}

func restProject(assertion string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<con:soapui-project name="Fixture" xmlns:con="http://eviware.com/soapui/config">
  <con:interface name="R" type="rest" basePath="/v1">
    <con:endpoints><con:endpoint>svc-a</con:endpoint></con:endpoints>
    <con:resource name="Things" path="/things">
      <con:method name="List" method="GET"/>
    </con:resource>
  </con:interface>
  <con:testSuite name="Suite">
    <con:testCase name="Case">
      <con:testStep type="restrequest" name="List">
        <con:config service="R" methodName="List" resourcePath="/things">
          <con:restRequest name="List" mediaType="application/json">
            <con:endpoint>svc-a</con:endpoint>
            <con:request/>
            ` + assertion + `
          </con:restRequest>
        </con:config>
      </con:testStep>
    </con:testCase>
  </con:testSuite>
</con:soapui-project>`
}

func hasWarn(res *Result, sub string) bool {
	for _, w := range res.Warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func assertNoSecret(t *testing.T, res *Result, secret string) {
	t.Helper()
	text, err := res.Project.Marshal("project.yaml")
	if err != nil {
		t.Fatal(err)
	}
	blob := string(text) + "\n" + strings.Join(res.Warnings, "\n")
	if strings.Contains(blob, secret) {
		t.Fatal("import output contains a secret value")
	}
}

// TestSoapUIRecheckFindings covers the second review of PR #7: Groovy slashy
// strings, credentials in schema definitions, and namespaced assertion markup.
func TestSoapUIRecheckFindings(t *testing.T) {
	for _, in := range []string{
		`a${= def p = /}/; "s3cr3t" }b`,
		`a${= def p = $/}/$; "s3cr3t" }b`,
		`a${= x = "q" / 2; "s3cr3t" }b`,
	} {
		out, removed := stripGroovyExprs(in)
		if !removed || strings.Contains(out, "s3cr3t") || strings.Contains(out, "/") && !strings.HasPrefix(out, "a") {
			t.Errorf("%q -> %q", in, out)
		}
	}
	if out, _ := stripGroovyExprs(`a${= def p = /}/; "s3cr3t" }b`); out != "ab" {
		t.Errorf("slashy string: got %q, want %q", out, "ab")
	}

	xmlDoc := `<r xmlns:ns="urn:x"><content><ns:id a="1">A</ns:id></content></r>`
	d := xml.NewDecoder(strings.NewReader(xmlDoc))
	for {
		tok, err := d.Token()
		if err != nil {
			t.Fatal(err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "content" {
			got, err := readXMLScalar(d, se)
			if err != nil {
				t.Fatal(err)
			}
			if got != `<id xmlns="urn:x" a="1">A</id>` {
				t.Fatalf("namespaced markup: %q", got)
			}
			break
		}
	}
}

// TestSoapUINestedResources: a child resource extends its parent's path and
// inherits its parameters (review of PR #7, finding A4).
func TestSoapUINestedResources(t *testing.T) {
	res := importXML(t, `<?xml version="1.0" encoding="UTF-8"?>
<con:soapui-project name="Nested" xmlns:con="http://eviware.com/soapui/config">
  <con:interface name="R" type="rest">
    <con:endpoints><con:endpoint>https://api.test</con:endpoint></con:endpoints>
    <con:resource name="Users" path="/users">
      <con:parameters>
        <con:parameter><con:name>tenant</con:name><con:value>acme</con:value><con:style>QUERY</con:style></con:parameter>
      </con:parameters>
      <con:resource name="Orders" path="orders">
        <con:method name="ListOrders" method="GET">
          <con:parameters>
            <con:parameter><con:name>limit</con:name><con:value>5</con:value><con:style>QUERY</con:style></con:parameter>
          </con:parameters>
        </con:method>
      </con:resource>
    </con:resource>
  </con:interface>
  <con:testSuite name="Suite">
    <con:testCase name="Case">
      <con:testStep type="restrequest" name="List">
        <con:config service="R" methodName="ListOrders" resourcePath="/users/orders">
          <con:restRequest name="List" method="GET">
            <con:endpoint>https://api.test</con:endpoint>
            <con:request/>
          </con:restRequest>
        </con:config>
      </con:testStep>
    </con:testCase>
  </con:testSuite>
</con:soapui-project>`)
	st := res.Project.Suites[0].Cases[0].Steps[0]
	b, _ := json.Marshal(st.Request)
	got := string(b)
	for _, want := range []string{"/users/orders", "tenant", "acme", "limit"} {
		if !strings.Contains(got, want) {
			t.Errorf("nested resource request missing %q: %s", want, got)
		}
	}
}

func TestSoapUITransferTargetScope(t *testing.T) {
	for _, tc := range []struct{ target, scope string }{
		{"#TestCase#", ""}, {"", ""}, {"#TestSuite#", model.TransferScopeRun},
		{"#Project#", model.TransferScopeRun}, {"#Global#", model.TransferScopeRun},
	} {
		imp := &suImporter{res: &Result{}, warned: map[string]bool{}, vars: map[string]string{}}
		spec, ok := imp.transferSpec("s/c/t", suTransfer{Name: "tok", SourceType: "Response", SourcePath: "$.token", TargetType: "token", TargetStep: tc.target})
		if !ok || spec.Scope != tc.scope || spec.Name != "token" {
			t.Errorf("target %q: got %+v ok=%v, want scope %q", tc.target, spec, ok, tc.scope)
		}
	}
}
