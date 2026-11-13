package importers

// Independent clean-room importer. Not affiliated with or endorsed by
// SmartBear. Reads SoapUI OSS (EUPL) project files; contains no SoapUI code.
// See docs/proposals/soapui-import.md.

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/verr"
)

// ImportSoapUI converts a SoapUI project file into a Verity project.
// SOAP request steps and REST request steps become steps, project properties
// and interface endpoints become the default environment, and anything that
// cannot be represented is listed in Result.Warnings. Literal secrets are
// replaced with ${env:NAME} references; warnings name the variable and never
// the value.
// soapuiMaxBytes is the largest project file ImportSoapUI will read.
// The check runs before the XML decoder so a huge local file cannot exhaust
// memory in the CLI or the GUI preview.
var soapuiMaxBytes int64 = 50 << 20

func ImportSoapUI(path string) (*Result, error) {
	data, err := readSoapUI(path)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	root, err := soapuiRoot(data)
	if err != nil {
		return nil, verr.Wrap(verr.CodeImport, err, "parse SoapUI project XML")
	}
	if root.Local != "soapui-project" {
		return nil, verr.Newf(verr.CodeImport, "not a SoapUI project (root element %q)", root.Local)
	}
	if root.Space != soapuiNS {
		return nil, verr.Newf(verr.CodeImport, "not a SoapUI project (namespace %q)", root.Space)
	}
	var doc suProject
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, verr.Wrap(verr.CodeImport, err, "parse SoapUI project")
	}

	imp := &suImporter{
		res:      &Result{},
		warned:   map[string]bool{},
		vars:     map[string]string{},
		byValue:  map[string]string{},
		envLits:  map[string]string{},
		ifaces:   map[string]*suIface{},
		authKeys: map[string]string{},
		authUsed: map[string]int{},
	}
	name := strings.TrimSpace(doc.Name)
	if name == "" {
		name = "Imported SoapUI Project"
	}
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) {
		base = "project.xml"
	}
	project := &model.Project{
		SchemaVersion: "1.0",
		Name:          name,
		Description:   "Generated from SoapUI project " + base,
		Environments: []model.Environment{{
			Name:      "default",
			Default:   true,
			Variables: imp.vars,
		}},
	}

	imp.assignVars("project", imp.vars, doc.Properties.Property)
	imp.warnProjectExtras(doc)
	imp.indexInterfaces(doc.Interfaces)

	suiteNames := map[string]int{}
	for _, ts := range doc.TestSuites {
		suite, ok := imp.buildSuite(ts, suiteNames)
		if !ok {
			continue
		}
		project.Suites = append(project.Suites, suite)
	}
	if len(project.Suites) == 0 {
		return nil, verr.New(verr.CodeImport, "SoapUI project contained no SOAP or REST requests")
	}
	project.Auth = imp.auths
	imp.res.Project = project
	return imp.res, nil
}

func readSoapUI(path string) ([]byte, error) {
	return readImportFile(path, "SoapUI project", soapuiMaxBytes)
}

// importMaxBytes is the largest OpenAPI, WSDL, or Postman file an importer
// reads, so a huge local file cannot exhaust memory in the CLI or the GUI
// preview. It is a variable so tests can lower it.
var importMaxBytes int64 = 50 << 20

// readImportFile reads at most max bytes of path; a larger file is an
// import_error. The limit is enforced while reading, not only from Stat.
func readImportFile(path, what string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, verr.Wrap(verr.CodeImport, err, "read "+what)
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > max {
		return nil, verr.Newf(verr.CodeImport, "%s is %d bytes, over the %d byte limit", what, fi.Size(), max)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, verr.Wrap(verr.CodeImport, err, "read "+what)
	}
	if int64(len(data)) > max {
		return nil, verr.Newf(verr.CodeImport, "%s is over the %d byte limit", what, max)
	}
	return data, nil
}

func soapuiRoot(data []byte) (xml.Name, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return xml.Name{}, err
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name, nil
		}
	}
}

type suImporter struct {
	res      *Result
	warned   map[string]bool
	vars     map[string]string
	byValue  map[string]string // literal endpoint -> environment variable name
	envLits  map[string]string
	ifaces   map[string]*suIface
	auths    []model.AuthDef
	authKeys map[string]string
	authUsed map[string]int
}

type suIface struct {
	basePath     string
	soapVersion  string
	rawEndpoints []string
	operations   map[string]string
	methods      []suIfaceMethod
	hasParams    bool
}

type suIfaceMethod struct {
	name   string
	path   string
	verb   string
	params []suParam
}

func (f *suIface) findMethod(methodName, resourcePath string) *suIfaceMethod {
	if f == nil {
		return nil
	}
	methodName = strings.TrimSpace(methodName)
	resourcePath = strings.TrimSpace(resourcePath)
	var nameOnly *suIfaceMethod
	for i := range f.methods {
		m := &f.methods[i]
		if methodName != "" && m.name != methodName {
			continue
		}
		if resourcePath != "" && m.path != "" && m.path != resourcePath {
			if methodName != "" && m.name == methodName && nameOnly == nil {
				nameOnly = m
			}
			continue
		}
		if methodName == "" && resourcePath == "" {
			continue
		}
		return m
	}
	return nameOnly
}

func (f *suIface) lookup(methodName, resourcePath string) (verb, path string, ok bool) {
	m := f.findMethod(methodName, resourcePath)
	if m == nil {
		return "", "", false
	}
	return m.verb, m.path, true
}

func (imp *suImporter) warn(msg string) {
	if imp.warned[msg] {
		return
	}
	imp.warned[msg] = true
	imp.res.Warnings = append(imp.res.Warnings, msg)
}

func (imp *suImporter) warnProjectExtras(doc suProject) {
	for _, m := range doc.MockServices {
		imp.warn(fmt.Sprintf("project: mock service %q not converted", named(m.Name)))
	}
	for _, m := range doc.RESTMocks {
		imp.warn(fmt.Sprintf("project: REST mock service %q not converted", named(m.Name)))
	}
	if strings.TrimSpace(doc.OAuth2.Inner) != "" {
		imp.warn("project: OAuth 2 profile not converted")
	}
	if strings.TrimSpace(doc.WSS.Inner) != "" {
		imp.warn("project: WS-Security configuration not converted")
	}
	imp.warnScripts("project", doc.SetupScript, doc.TearDownScript)
	if strings.TrimSpace(doc.Settings.Inner) != "" {
		imp.warn("project: settings not converted")
	}
}

func (imp *suImporter) warnScripts(scope, setup, tear string) {
	if strings.TrimSpace(setup) != "" {
		imp.warn(scope + ": setup script not converted")
	}
	if strings.TrimSpace(tear) != "" {
		imp.warn(scope + ": teardown script not converted")
	}
}

func named(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unnamed"
	}
	return s
}

func (imp *suImporter) indexInterfaces(list []suInterface) {
	for _, in := range list {
		name := strings.TrimSpace(in.Name)
		where := "interface " + named(name)
		if name == "" {
			imp.warn("project: interface has no name; skipped")
			continue
		}
		if _, exists := imp.ifaces[name]; exists {
			imp.warn(fmt.Sprintf("%s: duplicate interface name; kept the last one", where))
		}
		info := &suIface{
			basePath:     strings.TrimSpace(in.BasePath),
			operations:   map[string]string{},
			rawEndpoints: nil,
			hasParams:    len(in.Parameters.Params) > 0,
		}
		version, ok := normalizeSOAPVersion(in.SoapVersion)
		info.soapVersion = version
		if !ok {
			imp.warn(fmt.Sprintf("%s: SOAP version %q imported as 1.1", where, in.SoapVersion))
		}
		for _, ep := range in.Endpoints.Endpoint {
			ep = strings.TrimSpace(ep)
			if ep == "" {
				continue
			}
			info.rawEndpoints = append(info.rawEndpoints, ep)
			imp.endpointRef(where, ep)
		}
		for _, op := range in.Operations {
			opName := strings.TrimSpace(op.Name)
			if opName == "" {
				continue
			}
			info.operations[opName] = strings.TrimSpace(op.Action)
		}
		ifaceParams := foldParams(in.Parameters.Params)
		// Resources nest: a child extends its parent's path and inherits its
		// parameters, the same way a method inherits its resource's.
		var walk func(res suResource, parentPath string, parentParams []suParam)
		walk = func(res suResource, parentPath string, parentParams []suParam) {
			resPath := joinResourcePath(parentPath, strings.TrimSpace(res.Path))
			if len(res.Parameters.Params) > 0 {
				info.hasParams = true
			}
			resParams := mergeParams(parentParams, foldParams(res.Parameters.Params))
			if len(res.Methods) == 0 && len(res.Resources) == 0 && len(resParams) > 0 {
				info.methods = append(info.methods, suIfaceMethod{path: resPath, params: resParams})
			}
			for _, m := range res.Methods {
				if len(m.Parameters.Params) > 0 {
					info.hasParams = true
				}
				info.methods = append(info.methods, suIfaceMethod{
					name:   strings.TrimSpace(m.Name),
					path:   resPath,
					verb:   strings.ToUpper(strings.TrimSpace(m.Method)),
					params: mergeParams(resParams, foldParams(m.Parameters.Params)),
				})
			}
			for _, child := range res.Resources {
				walk(child, resPath, resParams)
			}
		}
		for _, res := range in.Resources {
			walk(res, "", ifaceParams)
		}
		imp.ifaces[name] = info
	}
}

func normalizeSOAPVersion(v string) (string, bool) {
	v = strings.TrimSpace(strings.ReplaceAll(v, "_", "."))
	switch v {
	case "", "1.1":
		return "1.1", true
	case "1.2":
		return "1.2", true
	default:
		return "1.1", false
	}
}

func (imp *suImporter) buildSuite(ts suTestSuite, suiteNames map[string]int) (model.Suite, bool) {
	name := uniqueName(suiteNames, defaultName(ts.Name, "Suite"))
	imp.warnScripts(name, ts.SetupScript, ts.TearDownScript)
	var cases []model.TestCase
	caseNames := map[string]int{}
	for _, tc := range ts.TestCases {
		c, ok := imp.buildCase(name, tc, caseNames)
		if ok {
			cases = append(cases, c)
		}
	}
	for _, lt := range ts.LoadTests {
		imp.warn(fmt.Sprintf("%s: load test %q not converted", name, named(lt.Name)))
	}
	for _, st := range ts.SecurityTests {
		imp.warn(fmt.Sprintf("%s: security test %q not converted", name, named(st.Name)))
	}
	if len(cases) == 0 {
		imp.warn(fmt.Sprintf("%s: no SOAP or REST steps; suite skipped", name))
		return model.Suite{}, false
	}
	suite := model.Suite{Name: name, Cases: cases}
	suite.Vars = imp.assignVars(name, nil, ts.Properties.Property)
	if isTrue(ts.Disabled) {
		imp.warn(fmt.Sprintf("%s: test suite is disabled in SoapUI; steps imported with skip", name))
		for ci := range suite.Cases {
			for si := range suite.Cases[ci].Steps {
				suite.Cases[ci].Steps[si].Skip = true
			}
		}
	}
	return suite, true
}

func (imp *suImporter) buildCase(suite string, tc suTestCase, caseNames map[string]int) (model.TestCase, bool) {
	name := uniqueName(caseNames, defaultName(tc.Name, "Case"))
	where := suite + "/" + name
	imp.warnScripts(where, tc.SetupScript, tc.TearDownScript)
	for _, lt := range tc.LoadTests {
		imp.warn(fmt.Sprintf("%s: load test %q not converted", where, named(lt.Name)))
	}
	for _, st := range tc.SecurityTests {
		imp.warn(fmt.Sprintf("%s: security test %q not converted", where, named(st.Name)))
	}

	var steps []model.Step
	index := map[string]int{}
	stepUsed := map[string]int{}
	var propBuf []suProperty
	carried := map[string]string{}
	var carriedAt []map[string]bool // per step: vars repeated from an earlier properties step
	var events []suEvent
	for _, st := range tc.TestSteps {
		raw := defaultName(st.Name, "Step")
		stepWhere := where + "/" + raw
		switch stepKind(st.Type) {
		case "properties":
			if isTrue(st.Disabled) {
				imp.warn(stepWhere + ": properties step is disabled; not imported")
				continue
			}
			events = append(events, suEvent{kind: "properties", name: raw})
			propBuf = append(propBuf, st.Config.Properties.Property...)
		case "transfer":
			if isTrue(st.Disabled) {
				imp.warn(stepWhere + ": transfer step is disabled; not imported")
				continue
			}
			events = append(events, suEvent{kind: "transfer", name: raw, transfers: append([]suTransfer(nil), st.Config.Transfers...)})
		case "soap", "rest":
			var step model.Step
			if stepKind(st.Type) == "soap" {
				step = imp.buildSOAP(stepWhere, st)
			} else {
				step = imp.buildREST(stepWhere, st)
			}
			if _, exists := index[raw]; exists {
				imp.warn(fmt.Sprintf("%s: duplicate step name %q", where, raw))
			} else {
				index[raw] = len(steps)
			}
			events = append(events, suEvent{kind: "request", name: raw, reqIdx: len(steps)})
			step.Name = uniqueName(stepUsed, raw)
			if len(propBuf) > 0 {
				step.Vars = imp.assignVars(stepWhere, step.Vars, propBuf)
				propBuf = nil
				for k, v := range step.Vars {
					carried[k] = v
				}
			}
			// Step vars are local to their step, but a SoapUI properties
			// step stays visible to every later request: repeat its values
			// on each later step.
			var carriedHere map[string]bool
			for k, v := range carried {
				if step.Vars == nil {
					step.Vars = map[string]string{}
				}
				if _, set := step.Vars[k]; !set {
					step.Vars[k] = v
					if carriedHere == nil {
						carriedHere = map[string]bool{}
					}
					carriedHere[k] = true
				}
			}
			carriedAt = append(carriedAt, carriedHere)
			if isTrue(tc.Disabled) {
				step.Skip = true
			}
			steps = append(steps, step)
		default:
			imp.warn(fmt.Sprintf("%s: %s", stepWhere, unsupportedStep(st.Type)))
		}
	}
	if len(steps) == 0 {
		if len(propBuf) > 0 {
			imp.warn(where + ": properties step not imported; case has no SOAP or REST request")
		}
		imp.warn(where + ": no SOAP or REST steps; case skipped")
		return model.TestCase{}, false
	}
	imp.attachTransfers(where, steps, index, events)
	// A value captured by a transfer replaces an earlier property of the
	// same name for the steps after the capture, as in SoapUI.
	for i := range steps {
		for _, t := range steps[i].Transfers {
			for k := i + 1; k < len(steps); k++ {
				if carriedAt[k][t.Name] {
					delete(steps[k].Vars, t.Name)
				}
			}
		}
	}
	imp.warnLateProperties(where, events, steps)
	c := model.TestCase{Name: name, Steps: steps}
	c.Vars = imp.assignVars(where, nil, tc.Properties.Property)
	if len(propBuf) > 0 {
		c.Vars = imp.assignVars(where, c.Vars, propBuf)
	}
	if isTrue(tc.Disabled) {
		imp.warn(where + ": test case is disabled in SoapUI; steps imported with skip")
	}
	return c, true
}

// suEvent is one test-step in SoapUI order. Verity cannot place a transfer
// anywhere except on its source request, so the original position is kept
// only to warn when that would change which requests see the variable.
type suEvent struct {
	kind      string
	name      string
	reqIdx    int
	transfers []suTransfer
}

// warnLateProperties reports properties steps that sit after the last
// request. Verity has no "run after the case" variable scope, so those
// values become case variables and earlier requests see them.
func (imp *suImporter) warnLateProperties(where string, events []suEvent, steps []model.Step) {
	lastReq := -1
	for i, ev := range events {
		if ev.kind == "request" {
			lastReq = i
		}
	}
	if lastReq < 0 {
		return
	}
	var propNames []string
	for _, ev := range events[lastReq+1:] {
		if ev.kind == "properties" {
			propNames = append(propNames, ev.name)
		}
	}
	if len(propNames) == 0 {
		return
	}
	stepNames := make([]string, len(steps))
	for i, st := range steps {
		stepNames[i] = st.Name
	}
	imp.warn(fmt.Sprintf("%s: properties step %s after the last request imported as case variables; %s will see them",
		where, strings.Join(propNames, ", "), strings.Join(stepNames, ", ")))
}

func stepKind(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "request":
		return "soap"
	case "restrequest":
		return "rest"
	case "properties":
		return "properties"
	case "transfer", "propertytransfers":
		return "transfer"
	default:
		return "other"
	}
}

func unsupportedStep(typeName string) string {
	switch strings.ToLower(strings.TrimSpace(typeName)) {
	case "groovy", "script":
		return "Groovy/script step not converted"
	case "jdbc", "datasource", "datasourceloop":
		return "JDBC/DataSource step not converted"
	case "jms", "jmspublish", "jmssubscribe", "publish", "subscribe":
		return "JMS step not converted"
	case "mockresponse", "restmockresponse":
		return "mock response step not converted"
	default:
		label := strings.TrimSpace(typeName)
		if label == "" {
			label = "unknown"
		}
		return label + " step not converted"
	}
}

func (imp *suImporter) buildSOAP(where string, st suTestStep) model.Step {
	call := st.Config.Request
	if call == nil {
		call = &suCall{}
	}
	iface := imp.ifaces[strings.TrimSpace(st.Config.Interface)]
	epRaw := call.Endpoint
	if strings.TrimSpace(epRaw) == "" && iface != nil && len(iface.rawEndpoints) > 0 {
		epRaw = iface.rawEndpoints[0]
	}
	url := imp.endpointRef(where, epRaw)
	if url == "" {
		url = "${ENDPOINT}"
		imp.warn(where + ": no endpoint; URL uses ${ENDPOINT}")
	}
	action := strings.TrimSpace(call.SOAPAction)
	if action == "" {
		action = strings.TrimSpace(call.WSA.Action)
	}
	version := "1.1"
	if iface != nil {
		if action == "" {
			action = iface.operations[strings.TrimSpace(st.Config.Operation)]
		}
		if iface.soapVersion != "" {
			version = iface.soapVersion
		}
	}
	action = imp.rewrite(where, action)
	req := map[string]any{"url": url, "version": version}
	if action != "" {
		req["soapAction"] = action
	}
	if body := imp.body(where, call.Body); body != "" {
		req["body"] = body
	}
	headers := imp.headers(where, call)
	if len(headers) > 0 {
		req["headers"] = toAnyMap(headers)
	}
	imp.noteCallExtras(where, call)
	step := model.Step{
		Protocol:   "soap",
		Request:    req,
		Assertions: imp.mapAssertions(where, call.Assertions, "soap"),
		Skip:       isTrue(st.Disabled),
	}
	if isTrue(st.Disabled) {
		imp.warn(where + ": disabled in SoapUI; imported with skip")
	}
	if auth := imp.authFrom(where, call.Credentials); auth != "" {
		step.Auth = &model.AuthRef{Use: []string{auth}}
	}
	return step
}

func (imp *suImporter) buildREST(where string, st suTestStep) model.Step {
	call := st.Config.RestRequest
	if call == nil {
		call = &suCall{}
	}
	cfg := st.Config
	iface := imp.ifaces[strings.TrimSpace(cfg.Service)]
	resPath := strings.TrimSpace(cfg.ResourcePath)
	base := ""
	verb := ""
	if isHTTPMethod(call.Method) {
		verb = strings.ToUpper(strings.TrimSpace(call.Method))
	}
	if iface != nil {
		base = iface.basePath
		if v, p, ok := iface.lookup(cfg.MethodName, resPath); ok {
			if verb == "" {
				verb = v
			}
			if resPath == "" {
				resPath = p
			}
		}
	}
	if verb == "" {
		if isHTTPMethod(cfg.MethodName) {
			verb = strings.ToUpper(strings.TrimSpace(cfg.MethodName))
		} else {
			verb = "GET"
			if strings.TrimSpace(cfg.MethodName) != "" || strings.TrimSpace(call.Method) != "" {
				label := strings.TrimSpace(cfg.MethodName)
				if label == "" {
					label = strings.TrimSpace(call.Method)
				}
				imp.warn(fmt.Sprintf("%s: REST method %q not found; defaulted to GET", where, label))
			} else {
				imp.warn(where + ": REST method not set; defaulted to GET")
			}
		}
	}
	epRaw := call.Endpoint
	if strings.TrimSpace(epRaw) == "" && iface != nil && len(iface.rawEndpoints) > 0 {
		epRaw = iface.rawEndpoints[0]
	}
	ep := imp.endpointRef(where, epRaw)
	if ep == "" {
		ep = "${ENDPOINT}"
		imp.warn(where + ": no endpoint; URL uses ${ENDPOINT}")
	}
	path := imp.rewrite(where, resPath)
	headers := imp.headers(where, call)
	query := map[string]string{}
	params := call.Parameters.Params
	if iface != nil {
		if m := iface.findMethod(cfg.MethodName, resPath); m != nil {
			params = mergeParams(m.params, call.Parameters.Params)
		} else if iface.hasParams {
			imp.warn(where + ": REST parameters declared on the interface, resource, or method were not applied; the resource or method did not match")
		}
	}
	path = imp.applyParams(where, path, params, headers, query)
	path = looseTemplates(path)
	req := map[string]any{
		"method": verb,
		"url":    joinURL(ep, imp.rewrite(where, base), path),
	}
	if body := imp.body(where, call.Body); body != "" {
		req["body"] = body
		if mt := strings.TrimSpace(call.MediaType); mt != "" && !hasHeader(headers, "Content-Type") {
			headers["Content-Type"] = mt
		}
	}
	if len(headers) > 0 {
		req["headers"] = toAnyMap(headers)
	}
	if len(query) > 0 {
		req["query"] = toAnyMap(query)
	}
	step := model.Step{
		Protocol:   "rest",
		Request:    req,
		Assertions: imp.mapAssertions(where, call.Assertions, "rest"),
		Skip:       isTrue(st.Disabled),
	}
	if isTrue(st.Disabled) {
		imp.warn(where + ": disabled in SoapUI; imported with skip")
	}
	if auth := imp.authFrom(where, call.Credentials); auth != "" {
		step.Auth = &model.AuthRef{Use: []string{auth}}
	}
	imp.noteCallExtras(where, call)
	return step
}

func (imp *suImporter) noteCallExtras(where string, call *suCall) {
	if call == nil {
		return
	}
	if len(call.Attachments) > 0 {
		imp.warn(where + ": attachments not imported")
	}
	for _, item := range call.Settings.Items {
		if strings.HasSuffix(item.ID, "@request-headers") {
			continue
		}
		imp.warn(where + ": request settings other than headers not imported")
		return
	}
}

func (imp *suImporter) applyParams(where, path string, params []suParam, headers, query map[string]string) string {
	for _, p := range params {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			if strings.TrimSpace(p.Value) != "" {
				imp.warn(where + ": parameter with no name not imported")
			}
			continue
		}
		style := strings.ToUpper(strings.TrimSpace(p.Style))
		var val string
		if style == "HEADER" {
			val = imp.headerValue(where, name, p.Value)
		} else {
			val = imp.scalarValue(where, "query", name, p.Value)
		}
		token := "{" + name + "}"
		inPath := strings.Contains(path, token)
		switch style {
		case "HEADER":
			imp.putHeader(where, headers, name, val)
		case "MATRIX":
			imp.warn(fmt.Sprintf("%s: matrix parameter %q not converted", where, name))
		case "QUERY":
			imp.putQuery(where, query, name, val)
		case "TEMPLATE", "PLAIN", "":
			if style == "PLAIN" {
				how := "a query parameter"
				if inPath {
					how = "a path value"
				}
				imp.warn(fmt.Sprintf("%s: parameter %q style PLAIN imported as %s", where, name, how))
			}
			if inPath || style == "TEMPLATE" {
				if !inPath {
					imp.warn(fmt.Sprintf("%s: template parameter %q not found in the path", where, name))
				} else {
					path = strings.ReplaceAll(path, token, val)
				}
			} else {
				imp.putQuery(where, query, name, val)
			}
		default:
			imp.warn(fmt.Sprintf("%s: parameter %q style %q not converted", where, name, p.Style))
		}
	}
	return path
}

func (imp *suImporter) putHeader(where string, headers map[string]string, name, val string) {
	if hasHeader(headers, name) {
		imp.warn(fmt.Sprintf("%s: duplicate header %q; kept the last value", where, name))
	}
	headers[name] = val
}

func (imp *suImporter) putQuery(where string, query map[string]string, name, val string) {
	if _, ok := query[name]; ok {
		imp.warn(fmt.Sprintf("%s: duplicate query parameter %q; kept the last value", where, name))
	}
	query[name] = val
}

func (imp *suImporter) headers(where string, call *suCall) map[string]string {
	out := map[string]string{}
	for _, item := range call.Settings.Items {
		if !strings.HasSuffix(item.ID, "@request-headers") {
			continue
		}
		pairs, err := parseHeaderEntries(item.Text)
		if err != nil {
			imp.warn(where + ": request headers could not be parsed")
			continue
		}
		for _, kv := range pairs {
			imp.putHeader(where, out, kv.key, imp.headerValue(where, kv.key, kv.value))
		}
	}
	return out
}

type headerKV struct {
	key   string
	value string
}

func parseHeaderEntries(s string) ([]headerKV, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	dec := xml.NewDecoder(strings.NewReader(s))
	var out []headerKV
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "entry" {
			continue
		}
		var k, v string
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "key":
				k = a.Value
			case "value":
				v = a.Value
			}
		}
		if k != "" {
			out = append(out, headerKV{key: k, value: v})
		}
	}
}

func (imp *suImporter) body(where, raw string) string {
	body := strings.TrimSpace(imp.scrubBody(where, imp.rewrite(where, raw)))
	return body
}

func (imp *suImporter) attachTransfers(where string, steps []model.Step, index map[string]int, events []suEvent) {
	seen := map[string]bool{}
	for ti, ev := range events {
		if ev.kind != "transfer" {
			continue
		}
		for _, tr := range ev.transfers {
			imp.attachOneTransfer(where, steps, index, events, ti, tr, seen)
		}
	}
}

func (imp *suImporter) attachOneTransfer(where string, steps []model.Step, index map[string]int, events []suEvent, ti int, tr suTransfer, seen map[string]bool) {
	src := strings.TrimSpace(tr.SourceStep)
	label := strings.TrimSpace(tr.Name)
	if label == "" {
		label = "transfer"
	}
	idx, ok := index[src]
	if !ok {
		imp.warn(fmt.Sprintf("%s: transfer %q source step %q not found in this case", where, label, src))
		return
	}
	spec, ok := imp.transferSpec(where, tr)
	if !ok {
		return
	}
	imp.warnTransferOrder(where, events, ti, idx, spec.Name, steps)
	if seen[idxName(idx, spec.Name)] {
		imp.warn(fmt.Sprintf("%s: duplicate transfer into %q; kept the last one", where, spec.Name))
	}
	seen[idxName(idx, spec.Name)] = true
	// Replace an earlier transfer with the same variable name.
	replaced := false
	for i := range steps[idx].Transfers {
		if steps[idx].Transfers[i].Name == spec.Name {
			steps[idx].Transfers[i] = spec
			replaced = true
			break
		}
	}
	if !replaced {
		steps[idx].Transfers = append(steps[idx].Transfers, spec)
	}
}

// warnTransferOrder reports when attaching a transfer to its source request
// cannot preserve SoapUI's step order. Verity transfers read that step's
// response, so they cannot be delayed until the transfer step.
func (imp *suImporter) warnTransferOrder(where string, events []suEvent, ti, srcIdx int, varName string, steps []model.Step) {
	srcEv := -1
	for i, ev := range events {
		if ev.kind == "request" && ev.reqIdx == srcIdx {
			srcEv = i
			break
		}
	}
	if srcEv < 0 {
		return
	}
	srcName := steps[srcIdx].Name
	if ti < srcEv {
		imp.warn(fmt.Sprintf("%s: transfer %q is before source step %q; Verity applies it when %q finishes, so %s is not available until then",
			where, varName, srcName, srcName, varName))
		return
	}
	var early []string
	for _, ev := range events[srcEv+1 : ti] {
		if ev.kind != "request" {
			continue
		}
		early = append(early, steps[ev.reqIdx].Name)
	}
	if len(early) == 0 {
		return
	}
	imp.warn(fmt.Sprintf("%s: transfer %q from step %q is applied when %q finishes, so %s sees %s before the transfer step",
		where, varName, srcName, srcName, strings.Join(early, ", "), varName))
}

func idxName(idx int, name string) string {
	return strconv.Itoa(idx) + "\x00" + name
}

func (imp *suImporter) transferSpec(where string, tr suTransfer) (model.TransferSpec, bool) {
	label := strings.TrimSpace(tr.Name)
	if label == "" {
		label = "transfer"
	}
	path := imp.rewrite(where, tr.SourcePath)
	from, expr, ok, note := classifyTransfer(tr.SourceType, path)
	if !ok {
		imp.warn(fmt.Sprintf("%s: transfer %q %s", where, label, note))
		return model.TransferSpec{}, false
	}
	if note != "" {
		imp.warn(fmt.Sprintf("%s: transfer %q %s", where, label, note))
	}
	varName := plainVar(imp.rewrite(where, tr.TargetType))
	targetStep := strings.TrimSpace(tr.TargetStep)
	lower := strings.ToLower(strings.TrimSpace(tr.TargetType))
	if varName == "" || lower == "request" || lower == "response" {
		varName = plainVar(imp.rewrite(where, tr.Name))
	}
	if varName == "" || strings.Contains(varName, "${") {
		imp.warn(fmt.Sprintf("%s: transfer %q has no target property name", where, label))
		return model.TransferSpec{}, false
	}
	scope := ""
	switch strings.ToLower(targetStep) {
	case "", "#testcase#":
	case "#testsuite#", "#project#", "#global#":
		// Suite/project/global properties outlive the test case: later
		// cases read them, which is what scope: run gives.
		scope = model.TransferScopeRun
	default:
		if strings.TrimSpace(tr.TargetPath) != "" || lower == "request" || lower == "response" {
			imp.warn(fmt.Sprintf("%s: transfer %q targets step %q; imported as variable %s (request bodies are not patched)", where, label, targetStep, varName))
		}
	}
	if strings.TrimSpace(tr.TargetPath) != "" && (strings.EqualFold(targetStep, "#TestCase#") || targetStep == "") {
		imp.warn(fmt.Sprintf("%s: transfer %q target path is not applied; only variable %s is set", where, label, varName))
	}
	return model.TransferSpec{Name: varName, From: from, Expr: strings.TrimSpace(expr), Scope: scope}, true
}

func classifyTransfer(sourceType, path string) (from, expr string, ok bool, note string) {
	path = strings.TrimSpace(path)
	switch strings.ToLower(strings.TrimSpace(sourceType)) {
	case "request":
		return "", "", false, "reads the request, which Verity transfers cannot do"
	case "xquery":
		return "xpath", path, true, "XQuery imported as XPath"
	case "xpath":
		return "xpath", path, true, ""
	case "json", "jsonpath":
		if path != "" && !strings.HasPrefix(path, "$") {
			path = "$." + path
		}
		return "jsonpath", path, true, ""
	case "", "response", "responseasxml", "response as xml":
		return classifyPath(path)
	default:
		if path == "" {
			return "", "", false, fmt.Sprintf("source type %q is not converted", sourceType)
		}
		from, expr, ok, _ = classifyPath(path)
		return from, expr, ok, fmt.Sprintf("source type %q imported as %s", sourceType, from)
	}
}

func classifyPath(path string) (from, expr string, ok bool, note string) {
	if path == "" {
		return "body", "", true, ""
	}
	if strings.HasPrefix(path, "$") {
		return "jsonpath", path, true, ""
	}
	return "xpath", path, true, ""
}

func (imp *suImporter) mapAssertions(where string, items []suAssertion, protocol string) []model.Assertion {
	var out []model.Assertion
	for _, a := range items {
		label := strings.TrimSpace(a.Type)
		if label == "" {
			label = strings.TrimSpace(a.Name)
		}
		if label == "" {
			label = "assertion"
		}
		if isTrue(a.Disabled) {
			imp.warn(fmt.Sprintf("%s: assertion %q is disabled; not imported", where, label))
			continue
		}
		mapped, reason := imp.mapAssertion(where, a, protocol)
		if reason != "" {
			imp.warn(fmt.Sprintf("%s: assertion %q not converted: %s", where, label, reason))
			continue
		}
		out = append(out, mapped...)
	}
	if len(items) > 0 && len(out) == 0 {
		imp.warn(where + ": no assertions converted; step kept")
	}
	return out
}

func (imp *suImporter) mapAssertion(where string, a suAssertion, protocol string) ([]model.Assertion, string) {
	desc := strings.TrimSpace(a.Name)
	if desc == "" {
		desc = strings.TrimSpace(a.Type)
	}
	switch strings.ToLower(strings.TrimSpace(a.Type)) {
	case "valid http status codes":
		return imp.statusCodes(where, a.Config.Codes, desc, false)
	case "invalid http status codes":
		return imp.statusCodes(where, a.Config.Codes, desc, true)
	case "response sla":
		n, err := strconv.Atoi(strings.TrimSpace(a.Config.SLA))
		if err != nil {
			return nil, "Response SLA is not a number of milliseconds"
		}
		return []model.Assertion{{Type: "response_time", Op: "lte", Value: n, Description: desc}}, ""
	case "xpath match":
		return imp.matchAssertion(where, "xpath", a, desc)
	case "jsonpath match":
		return imp.matchAssertion(where, "jsonpath", a, desc)
	case "contains":
		return imp.containsAssertion(where, a, desc, false)
	case "not contains":
		return imp.containsAssertion(where, a, desc, true)
	case "schema compliance":
		return imp.schemaAssertion(where, a, desc, protocol)
	default:
		return nil, "unsupported assertion type"
	}
}

func (imp *suImporter) statusCodes(where, raw, desc string, invalid bool) ([]model.Assertion, string) {
	codes := splitCodes(raw)
	if len(codes) == 0 {
		return nil, "no status codes"
	}
	nums := make([]int, 0, len(codes))
	for _, c := range codes {
		n, err := strconv.Atoi(c)
		if err != nil {
			return nil, "status code is not a number"
		}
		nums = append(nums, n)
	}
	if invalid && len(nums) > 1 {
		imp.warn(fmt.Sprintf("%s: Invalid HTTP Status Codes list imported as separate status ne assertions", where))
		out := make([]model.Assertion, len(nums))
		for i, n := range nums {
			out[i] = model.Assertion{Type: "status", Op: "ne", Value: n, Description: desc}
		}
		return out, ""
	}
	if len(nums) == 1 {
		op := "eq"
		if invalid {
			op = "ne"
		}
		return []model.Assertion{{Type: "status", Op: op, Value: nums[0], Description: desc}}, ""
	}
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = strconv.Itoa(n)
	}
	return []model.Assertion{{
		Type:        "status",
		Op:          "matches",
		Value:       "^(" + strings.Join(parts, "|") + ")$",
		Description: desc,
	}}, ""
}

func splitCodes(s string) []string {
	return strings.Fields(strings.ReplaceAll(s, ",", " "))
}

func (imp *suImporter) matchAssertion(where, typ string, a suAssertion, desc string) ([]model.Assertion, string) {
	expr := strings.TrimSpace(imp.rewrite(where, a.Config.Path))
	if expr == "" {
		return nil, "expression is empty"
	}
	if isTrue(a.Config.AllowWildcards) {
		imp.warn(where + ": " + a.Type + " wildcards are matched literally")
	}
	if isTrue(a.Config.IgnoreNS) {
		imp.warn(where + ": " + a.Type + " does not ignore namespace differences")
	}
	if isTrue(a.Config.IgnoreComments) {
		imp.warn(where + ": " + a.Type + " does not ignore comments")
	}
	content := imp.assertionText(where, a.Config.Content)
	if content == "" {
		return []model.Assertion{{Type: typ, Expr: expr, Op: "exists", Description: desc}}, ""
	}
	return []model.Assertion{{Type: typ, Expr: expr, Op: "eq", Value: content, Description: desc}}, ""
}

func (imp *suImporter) containsAssertion(where string, a suAssertion, desc string, negate bool) ([]model.Assertion, string) {
	token := imp.assertionText(where, a.Config.Token)
	if token == "" {
		return nil, "token is empty"
	}
	label := "Contains"
	if negate {
		label = "Not Contains"
	}
	if isTrue(a.Config.IgnoreCase) {
		imp.warn(fmt.Sprintf("%s: %s ignoreCase is not applied", where, label))
	}
	if isTrue(a.Config.UseRegEx) && !negate {
		imp.warn(where + ": Contains useRegEx imported as regex")
		return []model.Assertion{{Type: "regex", Expr: token, Description: desc}}, ""
	}
	if isTrue(a.Config.UseRegEx) && negate {
		imp.warn(where + ": Not Contains useRegEx is not applied; the token is matched literally")
	}
	as := model.Assertion{Type: "contains", Value: token, Description: desc}
	if negate {
		as.Op = "not_exists"
	}
	return []model.Assertion{as}, ""
}

func (imp *suImporter) schemaAssertion(where string, a suAssertion, desc, protocol string) ([]model.Assertion, string) {
	def := strings.TrimSpace(imp.scrubBody(where, imp.rewrite(where, a.Config.Definition)))
	if protocol == "rest" {
		if def == "" {
			return nil, "no JSON Schema in the configuration"
		}
		if strings.HasPrefix(def, "{") || strings.HasPrefix(def, "[") {
			return []model.Assertion{{
				Type:        "json_schema",
				Description: desc,
				Config:      map[string]any{"schema": def},
			}}, ""
		}
		if strings.Contains(def, "<") || strings.ContainsAny(def, "\n") || secrets.LooksLikeSecret(def) {
			return nil, "definition is not a JSON Schema"
		}
		// The definition may be a URL with user:pass@ or a key in its query.
		def = imp.scrubEndpoint(where, def)
		imp.warn(fmt.Sprintf("%s: Schema Compliance file is not copied; place it next to the generated project (the runner reads it from the project directory, not the SoapUI file)", where))
		return []model.Assertion{{Type: "json_schema", Expr: def, Description: desc}}, ""
	}
	imp.warn(where + ": Schema Compliance imported as XML well-formedness only")
	return []model.Assertion{{Type: "xsd", Description: desc}}, ""
}

func (imp *suImporter) authFrom(where string, c suCredentials) string {
	kind := strings.ToLower(strings.TrimSpace(c.AuthType))
	user := strings.TrimSpace(c.Username)
	pass := c.Password
	switch kind {
	case "", "no authorization", "none":
		if kind == "" && (user != "" || strings.TrimSpace(pass) != "") {
			return imp.basicAuth(where, user, pass)
		}
		return ""
	case "global http settings":
		imp.warn(where + ": Global HTTP Settings auth is not imported")
		return ""
	case "preemptive", "basic", "basic auth":
		return imp.basicAuth(where, user, pass)
	default:
		imp.warn(fmt.Sprintf("%s: auth type %q not converted", where, strings.TrimSpace(c.AuthType)))
		return ""
	}
}

func (imp *suImporter) basicAuth(where, user, pass string) string {
	userOut := imp.rewrite(where, strings.TrimSpace(user))
	passOut := imp.rewrite(where, pass)
	if strings.TrimSpace(userOut) == "" {
		imp.warn(where + ": basic auth has no username; not imported")
		return ""
	}
	if soapHasLiteral(userOut) && secrets.LooksLikeSecret(strings.TrimSpace(stripVerityRefs(userOut))) {
		env := imp.envRef("username", userOut)
		imp.warn(fmt.Sprintf("%s: literal username not imported; set environment variable %s", where, envName(env)))
		userOut = env
	}
	// A password with any literal text is replaced wholesale, including
	// mixed values such as "literal-${suffix}". Pure ${...} templates stay.
	if passOut == "" || soapHasLiteral(passOut) {
		env := imp.envRef("password", passOut)
		if strings.TrimSpace(passOut) == "" {
			imp.warn(fmt.Sprintf("%s: password is empty; set environment variable %s", where, envName(env)))
		} else {
			imp.warn(fmt.Sprintf("%s: literal password not imported; set environment variable %s", where, envName(env)))
		}
		passOut = env
	}
	key := userOut + "\x00" + passOut
	if name, ok := imp.authKeys[key]; ok {
		return name
	}
	base := "basic"
	if !strings.Contains(userOut, "${") {
		suffix := strings.Trim(strings.ToLower(reEnvName.ReplaceAllString(userOut, "_")), "_")
		if suffix != "" {
			base = "basic_" + suffix
		}
	}
	name := uniqueName(imp.authUsed, base)
	imp.authKeys[key] = name
	imp.auths = append(imp.auths, model.AuthDef{
		Name:   name,
		Type:   "basic",
		Config: map[string]any{"username": userOut, "password": passOut},
	})
	return name
}

// endpointRef turns an endpoint into a ${VAR} reference when the whole value
// is a literal. Mixed text such as ${ENDPOINT}/soap is rewritten in place.
// Interface endpoints are allocated ENDPOINT, ENDPOINT_1, ENDPOINT_2, ...
// in file order, skipping names already used by project properties.
func (imp *suImporter) endpointRef(where, raw string) string {
	rewritten := imp.scrubEndpoint(where, imp.rewrite(where, strings.TrimSpace(raw)))
	if rewritten == "" {
		return ""
	}
	if reSingleVar.MatchString(rewritten) || strings.Contains(rewritten, "${") {
		return rewritten
	}
	if name, ok := imp.byValue[rewritten]; ok {
		return "${" + name + "}"
	}
	var names []string
	for name, val := range imp.vars {
		if val == rewritten {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		return "${" + names[0] + "}"
	}
	return "${" + imp.internEndpoint(rewritten) + "}"
}

func (imp *suImporter) internEndpoint(value string) string {
	if name, ok := imp.byValue[value]; ok {
		return name
	}
	name := "ENDPOINT"
	for n := 0; ; n++ {
		if n > 0 {
			name = fmt.Sprintf("ENDPOINT_%d", n)
		}
		if _, exists := imp.vars[name]; !exists {
			break
		}
	}
	imp.vars[name] = value
	imp.byValue[value] = name
	return name
}

func (imp *suImporter) assignVars(where string, dst map[string]string, props []suProperty) map[string]string {
	for _, p := range props {
		if strings.TrimSpace(p.Name) == "" {
			if strings.TrimSpace(p.Value) != "" {
				imp.warn(where + ": property with no name not imported")
			}
			continue
		}
		if dst == nil {
			dst = map[string]string{}
		}
		if _, ok := dst[p.Name]; ok {
			imp.warn(fmt.Sprintf("%s: duplicate property %q; kept the last value", where, p.Name))
		}
		dst[p.Name] = imp.scalarValue(where, "property", p.Name, p.Value)
	}
	return dst
}

func (imp *suImporter) headerValue(where, name, v string) string {
	v = imp.rewrite(where, v)
	if v == "" || !soapHasLiteral(v) {
		return v
	}
	prefix := ""
	cred := v
	if scheme, rest, ok := strings.Cut(v, " "); ok && reAuthScheme.MatchString(scheme) && rest != "" {
		prefix, cred = scheme+" ", rest
	}
	if !soapHasLiteral(cred) {
		return v
	}
	if !secretMaterial(name, cred) && !reSecretHeader.MatchString(name) {
		return v
	}
	env := imp.envRef(name, cred)
	imp.warn(fmt.Sprintf("%s: header %q held a literal credential; set environment variable %s", where, name, envName(env)))
	return prefix + env
}

func (imp *suImporter) scalarValue(where, kind, key, v string) string {
	v = imp.rewrite(where, v)
	if v == "" || !soapHasLiteral(v) {
		return v
	}
	if !secretMaterial(key, v) {
		return v
	}
	env := imp.envRef(key, v)
	if kind == "property" {
		imp.warn(fmt.Sprintf("%s variable %q: secret value not imported; set environment variable %s", where, key, envName(env)))
		return env
	}
	imp.warn(fmt.Sprintf("%s: query parameter %q held a literal credential; set environment variable %s", where, key, envName(env)))
	return env
}

// secretMaterial reports whether a credential-named field, or a value that
// still contains literal text and looks like a secret, must leave the project.
// Mixed values such as "literal-${suffix}" count: any literal fragment does.
func secretMaterial(key, v string) bool {
	if !soapHasLiteral(v) {
		return false
	}
	if isSecretKey(key) || reSecretHeader.MatchString(key) {
		return true
	}
	return secrets.LooksLikeSecret(strings.TrimSpace(stripVerityRefs(v)))
}

func (imp *suImporter) assertionText(where, v string) string {
	v = strings.TrimSpace(imp.scrubBody(where, imp.rewrite(where, v)))
	if v == "" || !soapHasLiteral(v) {
		return v
	}
	lit := strings.TrimSpace(stripVerityRefs(v))
	if !secrets.LooksLikeSecret(lit) {
		return v
	}
	env := imp.envRef("assertion", v)
	imp.warn(fmt.Sprintf("%s: assertion expected value not imported; set environment variable %s", where, envName(env)))
	return env
}

func (imp *suImporter) scrubBody(where, body string) string {
	body = imp.scrubJSONSecrets(where, body)
	if strings.Contains(body, "<") {
		body = imp.scrubXMLSecrets(where, body)
	}
	return body
}

// reSUJSONField matches a credential-named JSON field and the opening quote
// of its string value. The names are the same set the Postman body scrubber
// covers. The value itself is read separately so a leading "$" or "{" is not
// skipped when it is not a template.
var reSUJSONField = regexp.MustCompile(`(?i)"(password|passwd|secret|token|access_token|refresh_token|client_secret|api[_-]?key|apikey)"\s*:\s*"`)

func (imp *suImporter) scrubJSONSecrets(where, body string) string {
	locs := reSUJSONField.FindAllStringSubmatchIndex(body, -1)
	for i := len(locs) - 1; i >= 0; i-- {
		loc := locs[i]
		name := body[loc[2]:loc[3]]
		valStart := loc[1]
		valEnd, ok := jsonStringEnd(body, valStart)
		if !ok {
			continue
		}
		raw := body[valStart:valEnd]
		val := raw
		if unq, err := strconv.Unquote(`"` + raw + `"`); err == nil {
			val = unq
		}
		if !soapHasLiteral(val) {
			continue
		}
		env := imp.envRef(name, val)
		imp.warn(fmt.Sprintf("%s: body field %q held a literal credential; set environment variable %s", where, name, envName(env)))
		body = body[:valStart] + env + body[valEnd:]
	}
	return body
}

func jsonStringEnd(s string, start int) (int, bool) {
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if esc {
			esc = false
			continue
		}
		if c == '\\' {
			esc = true
			continue
		}
		if c == '"' {
			return i, true
		}
	}
	return 0, false
}

func (imp *suImporter) envRef(base, literal string) string {
	name := strings.Trim(strings.ToUpper(reEnvName.ReplaceAllString(base, "_")), "_")
	if name == "" {
		name = "SECRET"
	}
	cand := name
	for i := 2; ; i++ {
		prev, ok := imp.envLits[cand]
		if !ok || prev == literal {
			break
		}
		cand = fmt.Sprintf("%s_%d", name, i)
	}
	imp.envLits[cand] = literal
	return "${env:" + cand + "}"
}

func (imp *suImporter) rewrite(where, s string) string {
	out, warns := rewriteExpr(s)
	for _, w := range warns {
		if where != "" {
			imp.warn(where + ": " + w)
		} else {
			imp.warn(w)
		}
	}
	return out
}

var (
	reScopeRef  = regexp.MustCompile(`\$\{#([A-Za-z][A-Za-z0-9_-]*)#([^}]*)\}`)
	reSingleVar = regexp.MustCompile(`^\$\{[^{}]+\}$`)
	reIdent     = regexp.MustCompile(`^[A-Za-z_][\w.-]*$`)
)

// rewriteExpr converts SoapUI property references to Verity ${name} references.
// ${#Project#name}, ${#TestCase#name}, and ${#TestSuite#name} become ${name}.
// ${#Global#name}, ${#System#name}, and ${#Env#name} also become ${name}, and
// the returned warnings say that those scopes are ordinary variables here.
// Inline ${= ...} expressions are removed in full, including nested braces
// and quoted text. The warning does not include the expression.
func rewriteExpr(s string) (string, []string) {
	var warns []string
	seen := map[string]bool{}
	add := func(w string) {
		if seen[w] {
			return
		}
		seen[w] = true
		warns = append(warns, w)
	}
	if out, removed := stripGroovyExprs(s); removed {
		add("inline Groovy expression not converted")
		s = out
	}
	s = reScopeRef.ReplaceAllStringFunc(s, func(m string) string {
		parts := reScopeRef.FindStringSubmatch(m)
		scope, name := parts[1], strings.TrimSpace(parts[2])
		if name == "" {
			add("empty property reference not converted")
			return m
		}
		switch strings.ToLower(scope) {
		case "project", "testcase", "testsuite":
		case "global":
			add(fmt.Sprintf("property ${#%s#%s} imported as ${%s}; SoapUI global properties become ordinary variables", scope, name, name))
		case "system":
			add(fmt.Sprintf("property ${#%s#%s} imported as ${%s}; SoapUI system properties become ordinary variables", scope, name, name))
		case "env":
			add(fmt.Sprintf("property ${#%s#%s} imported as ${%s}; SoapUI environment properties become ordinary variables", scope, name, name))
		default:
			add(fmt.Sprintf("property ${#%s#%s} imported as ${%s}; scope %q has no Verity equivalent", scope, name, name, scope))
		}
		return "${" + name + "}"
	})
	return s, warns
}

// stripGroovyExprs removes every balanced ${= ...} expression. Braces inside
// quotes, line comments, and block comments do not end the expression. An
// unclosed expression is dropped through the end of the string so its text
// cannot remain in the project. The removed text is not returned.
func stripGroovyExprs(s string) (string, bool) {
	var b strings.Builder
	removed := false
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "${=") {
			end := scanGroovy(s, i)
			removed = true
			if end < 0 {
				return b.String(), true
			}
			i = end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String(), removed
}

// scanGroovy returns the index just after the ${= ...} expression that
// starts at start, or -1 when the braces never balance.
func scanGroovy(s string, start int) int {
	// start is the '$' of `${=`. The '{' is already open.
	i := start + 3
	depth := 1
	var quote byte
	triple := false
	// prev is the last significant character outside strings and comments;
	// Groovy only starts a /slashy/ string where an expression can begin.
	prev := byte('{')
	for i < len(s) {
		c := s[i]
		if quote != 0 {
			if triple {
				if i+2 < len(s) && s[i] == quote && s[i+1] == quote && s[i+2] == quote {
					quote = 0
					prev = 'x'
					triple = false
					i += 3
					continue
				}
				i++
				continue
			}
			if c == '\\' && i+1 < len(s) {
				i += 2
				continue
			}
			if c == quote {
				quote = 0
				prev = 'x'
			}
			i++
			continue
		}
		if c == '$' && i+1 < len(s) && s[i+1] == '/' {
			// $/ dollar-slashy /$ string: ends at the first "/$" not escaped by '$'.
			i += 2
			for i < len(s) {
				if s[i] == '$' && i+1 < len(s) && (s[i+1] == '/' || s[i+1] == '$') {
					i += 2
					continue
				}
				if s[i] == '/' && i+1 < len(s) && s[i+1] == '$' {
					i += 2
					break
				}
				i++
			}
			prev = 'x'
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] != '/' && s[i+1] != '*' && strings.IndexByte("=(,:[!&|?{;~+-*%<>^", prev) >= 0 {
			// /slashy/ string: only '\/' escapes the delimiter.
			i++
			for i < len(s) && s[i] != '/' {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				i++
			}
			i++
			prev = 'x'
			continue
		}
		if c == '/' && i+1 < len(s) && (s[i+1] == '/' || s[i+1] == '*') {
			if s[i+1] == '/' {
				i += 2
				for i < len(s) && s[i] != '\n' {
					i++
				}
				continue
			}
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			if i+1 < len(s) {
				i += 2
			}
			continue
		}
		if c == '"' || c == '\'' {
			if i+2 < len(s) && s[i+1] == c && s[i+2] == c {
				quote = c
				triple = true
				i += 3
				continue
			}
			quote = c
			triple = false
			i++
			continue
		}
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				return i + 1
			}
		}
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			prev = c
		}
		i++
	}
	return -1
}

// soapHasLiteral reports whether v has any text outside ${...} templates.
// "literal-${suffix}" does. A value that is only ${name} does not.
func soapHasLiteral(v string) bool {
	return strings.TrimSpace(stripVerityRefs(v)) != ""
}

func stripVerityRefs(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); {
		if v[i] == '$' && i+1 < len(v) && v[i+1] == '{' {
			end := balancedBrace(v, i+1)
			if end > 0 {
				i = end
				continue
			}
		}
		b.WriteByte(v[i])
		i++
	}
	return b.String()
}

func balancedBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

func (imp *suImporter) scrubEndpoint(where, raw string) string {
	if raw == "" {
		return ""
	}
	out := raw
	if scheme, userinfo, rest, ok := splitEndpointUserinfo(raw); ok {
		user, pass, hasPass := splitUserPass(userinfo)
		changed := false
		if soapHasLiteral(user) {
			ref := imp.envRef("endpoint_user", user)
			imp.warn(fmt.Sprintf("%s: endpoint username not imported; set environment variable %s", where, envName(ref)))
			user = ref
			changed = true
		}
		if hasPass && soapHasLiteral(pass) {
			ref := imp.envRef("endpoint_password", pass)
			imp.warn(fmt.Sprintf("%s: endpoint password not imported; set environment variable %s", where, envName(ref)))
			pass = ref
			changed = true
		}
		if changed {
			ui := user
			if hasPass {
				ui += ":" + pass
			}
			out = scheme + "://" + ui + "@" + rest
		}
	}
	q := strings.IndexByte(out, '?')
	if q < 0 {
		return out
	}
	base, query := out[:q], out[q+1:]
	frag := ""
	if h := strings.IndexByte(query, '#'); h >= 0 {
		frag = query[h:]
		query = query[:h]
	}
	scrubbed, changed := imp.scrubRawQuery(where, query)
	if !changed {
		return out
	}
	return base + "?" + scrubbed + frag
}

func splitEndpointUserinfo(raw string) (scheme, userinfo, rest string, ok bool) {
	i := strings.Index(raw, "://")
	if i < 0 {
		return "", "", "", false
	}
	scheme = raw[:i]
	restAll := raw[i+3:]
	cut := len(restAll)
	for _, sep := range []byte{'/', '?', '#'} {
		if j := strings.IndexByte(restAll, sep); j >= 0 && j < cut {
			cut = j
		}
	}
	host := restAll[:cut]
	tail := restAll[cut:]
	at := strings.LastIndex(host, "@")
	if at < 0 {
		return scheme, "", restAll, false
	}
	return scheme, host[:at], host[at+1:] + tail, true
}

func splitUserPass(userinfo string) (user, pass string, hasPass bool) {
	user, pass, hasPass = strings.Cut(userinfo, ":")
	return user, pass, hasPass
}

func (imp *suImporter) scrubRawQuery(where, raw string) (string, bool) {
	if raw == "" {
		return raw, false
	}
	parts := strings.Split(raw, "&")
	changed := false
	for i, part := range parts {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key, err1 := url.QueryUnescape(k)
		val, err2 := url.QueryUnescape(v)
		if err1 != nil || err2 != nil || !soapHasLiteral(val) {
			continue
		}
		if !isSecretKey(key) && !secrets.LooksLikeSecret(strings.TrimSpace(stripVerityRefs(val))) {
			continue
		}
		env := imp.envRef(key, val)
		imp.warn(fmt.Sprintf("%s: endpoint query %q not imported; set environment variable %s", where, key, envName(env)))
		parts[i] = k + "=" + env
		changed = true
	}
	if !changed {
		return raw, false
	}
	return strings.Join(parts, "&"), true
}

func isCredentialXMLName(local string) bool {
	n := strings.ToLower(local)
	n = strings.ReplaceAll(n, "_", "")
	n = strings.ReplaceAll(n, "-", "")
	switch n {
	case "password", "passwd", "secret", "token", "accesstoken", "refreshtoken", "clientsecret", "apikey":
		return true
	default:
		return false
	}
}

func (imp *suImporter) scrubXMLSecrets(where, body string) string {
	var b strings.Builder
	i := 0
	for i < len(body) {
		if body[i] != '<' {
			b.WriteByte(body[i])
			i++
			continue
		}
		if strings.HasPrefix(body[i:], "<![CDATA[") {
			end := strings.Index(body[i:], "]]>")
			if end < 0 {
				b.WriteString(body[i:])
				break
			}
			b.WriteString(body[i : i+end+3])
			i += end + 3
			continue
		}
		if strings.HasPrefix(body[i:], "<!--") {
			end := strings.Index(body[i:], "-->")
			if end < 0 {
				b.WriteString(body[i:])
				break
			}
			b.WriteString(body[i : i+end+3])
			i += end + 3
			continue
		}
		if strings.HasPrefix(body[i:], "</") || strings.HasPrefix(body[i:], "<?") || strings.HasPrefix(body[i:], "<!") {
			end := tagEnd(body, i)
			if end < 0 {
				b.WriteString(body[i:])
				break
			}
			b.WriteString(body[i:end])
			i = end
			continue
		}
		end := tagEnd(body, i)
		if end < 0 {
			b.WriteString(body[i:])
			break
		}
		tag := body[i:end]
		local := elementLocal(tag)
		b.WriteString(imp.scrubAttrs(where, tag))
		i = end
		if isSelfClosing(tag) || !isCredentialXMLName(local) {
			continue
		}
		inner, after, ok := elementInner(body, i, local)
		if !ok {
			continue
		}
		text := visibleXMLText(inner)
		if soapHasLiteral(text) {
			env := imp.envRef(local, text)
			imp.warn(fmt.Sprintf("%s: body element %q held a literal credential; set environment variable %s", where, local, envName(env)))
			b.WriteString(env)
			b.WriteString(body[i+len(inner) : after])
		} else {
			b.WriteString(body[i:after])
		}
		i = after
	}
	return b.String()
}

func (imp *suImporter) scrubAttrs(where, tag string) string {
	var b strings.Builder
	last := 0
	for i := 0; i < len(tag); i++ {
		if tag[i] != '=' {
			continue
		}
		j := i - 1
		for j >= 0 && isXMLSpace(tag[j]) {
			j--
		}
		endName := j + 1
		for j >= 0 && isXMLNameChar(tag[j]) {
			j--
		}
		name := tag[j+1 : endName]
		if name == "" {
			continue
		}
		local := name
		if k := strings.LastIndex(local, ":"); k >= 0 {
			local = local[k+1:]
		}
		if !isCredentialXMLName(local) {
			continue
		}
		k := i + 1
		for k < len(tag) && isXMLSpace(tag[k]) {
			k++
		}
		if k >= len(tag) || (tag[k] != '"' && tag[k] != '\'') {
			continue
		}
		quote := tag[k]
		vstart := k + 1
		rel := strings.IndexByte(tag[vstart:], quote)
		if rel < 0 {
			break
		}
		vend := vstart + rel
		val := unescapeXMLText(tag[vstart:vend])
		if !soapHasLiteral(val) {
			i = vend
			continue
		}
		env := imp.envRef(local, val)
		imp.warn(fmt.Sprintf("%s: body attribute %q held a literal credential; set environment variable %s", where, local, envName(env)))
		b.WriteString(tag[last:vstart])
		b.WriteString(env)
		last = vend
		i = vend
	}
	b.WriteString(tag[last:])
	return b.String()
}

func tagEnd(s string, i int) int {
	quote := byte(0)
	for j := i; j < len(s); j++ {
		c := s[j]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == '>' {
			return j + 1
		}
	}
	return -1
}

func elementLocal(tag string) string {
	s := strings.TrimPrefix(tag, "</")
	s = strings.TrimPrefix(s, "<")
	if i := strings.IndexAny(s, " \t\r\n>/"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func isSelfClosing(tag string) bool {
	return strings.HasSuffix(strings.TrimSpace(tag), "/>")
}

func isXMLSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func isXMLNameChar(c byte) bool {
	return c == ':' || c == '_' || c == '-' || c == '.' ||
		(c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

func elementInner(s string, contentStart int, local string) (inner string, after int, ok bool) {
	depth := 1
	i := contentStart
	for i < len(s) {
		if strings.HasPrefix(s[i:], "<![CDATA[") {
			end := strings.Index(s[i:], "]]>")
			if end < 0 {
				return "", 0, false
			}
			i += end + 3
			continue
		}
		if strings.HasPrefix(s[i:], "<!--") {
			end := strings.Index(s[i:], "-->")
			if end < 0 {
				return "", 0, false
			}
			i += end + 3
			continue
		}
		if i+1 < len(s) && s[i] == '<' && s[i+1] == '/' {
			te := tagEnd(s, i)
			if te < 0 {
				return "", 0, false
			}
			if strings.EqualFold(elementLocal(s[i:te]), local) {
				depth--
				if depth == 0 {
					return s[contentStart:i], te, true
				}
			}
			i = te
			continue
		}
		if s[i] == '<' && i+1 < len(s) && isXMLNameChar(s[i+1]) {
			te := tagEnd(s, i)
			if te < 0 {
				return "", 0, false
			}
			if strings.EqualFold(elementLocal(s[i:te]), local) && !isSelfClosing(s[i:te]) {
				depth++
			}
			i = te
			continue
		}
		i++
	}
	return "", 0, false
}

func visibleXMLText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "<![CDATA[") {
			end := strings.Index(s[i:], "]]>")
			if end < 0 {
				b.WriteString(s[i:])
				break
			}
			b.WriteString(s[i+9 : i+end])
			i += end + 3
			continue
		}
		if s[i] == '<' {
			end := tagEnd(s, i)
			if end < 0 {
				b.WriteString(s[i:])
				break
			}
			i = end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return unescapeXMLText(b.String())
}

func unescapeXMLText(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	dec := xml.NewDecoder(strings.NewReader("<r>" + s + "</r>"))
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return b.String()
			}
			return s
		}
		if cd, ok := tok.(xml.CharData); ok {
			b.Write(cd)
		}
	}
}

// foldParams applies default values and keeps the last parameter of each name.
// Request parameters are overlaid later and may be explicitly empty.
func foldParams(params []suParam) []suParam {
	var out []suParam
	for _, p := range params {
		p.Name = strings.TrimSpace(p.Name)
		p.Style = strings.TrimSpace(p.Style)
		p.Value = strings.TrimSpace(p.Value)
		p.Default = strings.TrimSpace(p.Default)
		if p.Value == "" {
			p.Value = p.Default
		}
		out = mergeParams(out, []suParam{p})
	}
	return out
}

// mergeParams overlays over onto base by parameter name. An empty overlay
// value replaces the inherited value. An empty overlay style keeps the
// inherited style.
func mergeParams(base, over []suParam) []suParam {
	out := append([]suParam(nil), base...)
	for _, p := range over {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			out = append(out, p)
			continue
		}
		style := strings.TrimSpace(p.Style)
		found := false
		for i := range out {
			if strings.TrimSpace(out[i].Name) != name {
				continue
			}
			if style == "" {
				style = out[i].Style
			}
			out[i] = suParam{Name: name, Value: p.Value, Style: style}
			found = true
			break
		}
		if !found {
			out = append(out, suParam{Name: name, Value: p.Value, Style: style})
		}
	}
	return out
}

func plainVar(s string) string {
	s = strings.TrimSpace(s)
	if reSingleVar.MatchString(s) {
		return strings.TrimSuffix(strings.TrimPrefix(s, "${"), "}")
	}
	return s
}

func looseTemplates(path string) string {
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		if path[i] == '$' && i+1 < len(path) && path[i+1] == '{' {
			rel := strings.IndexByte(path[i+2:], '}')
			if rel >= 0 {
				end := i + 2 + rel
				b.WriteString(path[i : end+1])
				i = end
				continue
			}
		}
		if path[i] == '{' {
			rel := strings.IndexByte(path[i+1:], '}')
			if rel > 0 {
				name := path[i+1 : i+1+rel]
				if reIdent.MatchString(name) {
					b.WriteString("${")
					b.WriteString(name)
					b.WriteString("}")
					i += rel + 1
					continue
				}
			}
		}
		b.WriteByte(path[i])
	}
	return b.String()
}

func joinURL(parts ...string) string {
	var b strings.Builder
	var query string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "/" {
			continue
		}
		if q := strings.IndexByte(p, '?'); q >= 0 {
			if query == "" {
				query = p[q:]
			}
			p = p[:q]
		}
		// Keep a scheme's "://" intact. Trim(p, "/") would eat it from both ends
		// only when the whole string is slashes, but a query must stay at the end
		// after the resource path is joined.
		if strings.Contains(p, "://") {
			p = strings.TrimRight(p, "/")
		} else {
			p = strings.Trim(p, "/")
		}
		if p == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('/')
		}
		b.WriteString(p)
	}
	b.WriteString(query)
	return b.String()
}

func defaultName(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	return s
}

func isTrue(s string) bool {
	s = strings.TrimSpace(s)
	return strings.EqualFold(s, "true") || s == "1"
}

func isHTTPMethod(m string) bool {
	switch strings.ToUpper(strings.TrimSpace(m)) {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "TRACE":
		return true
	default:
		return false
	}
}

// joinResourcePath appends a child resource path to its parent's.
func joinResourcePath(parent, child string) string {
	if parent == "" {
		return child
	}
	if child == "" {
		return parent
	}
	return strings.TrimRight(parent, "/") + "/" + strings.TrimLeft(child, "/")
}
