package importers

// Independent importer for Postman Collection v2.0/v2.1 and environment
// exports. Not affiliated with or endorsed by Postman, Inc. See
// docs/proposals/postman-import.md for the mapping and its limits.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/secretscan"
	"github.com/pgodlews/verity-api/core/verr"
)

var (
	rePMVar        = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)
	rePMPureVar    = regexp.MustCompile(`^\{\{\s*([^{}$][^{}]*?)\s*\}\}$`)
	rePathVar      = regexp.MustCompile(`/:([A-Za-z_][\w-]*)`)
	reSecretKey    = regexp.MustCompile(`(?i)(passw|secret|token|api[_-]?key|private[_-]?key|credential)`)
	reNotSecretKey = regexp.MustCompile(`(?i)(url|uri|endpoint|host|type|name)$`)
	reSecretHeader = regexp.MustCompile(`(?i)^(authorization|proxy-authorization|cookie|x-api-key|api-key|apikey|x-auth-token|x-access-token)$`)
	reBodySecret   = regexp.MustCompile(`(?i)"(password|passwd|secret|token|access_token|refresh_token|client_secret|api[_-]?key|apikey)"\s*:\s*"([^"{}$][^"]*)"`)
	reVerityRef    = regexp.MustCompile(`\$\{([^}:]+)\}`)
	reEnvName      = regexp.MustCompile(`[^A-Za-z0-9]+`)
	reAuthScheme   = regexp.MustCompile(`^[A-Za-z]+$`)
)

// ImportPostman converts a Postman collection (v2.0/v2.1 JSON export) and any
// number of Postman environment exports into a Verity project. Each top-level
// folder becomes a suite and each folder's requests one case, so variables
// captured by one request remain visible to the next. A capture that a later
// folder uses gets scope: run. Anything that cannot be converted is reported in
// Result.Warnings; literal secrets are replaced with ${env:NAME} references.
func ImportPostman(path string, envPaths ...string) (*Result, error) {
	col, err := readPostmanCollection(path)
	if err != nil {
		return nil, err
	}
	imp := &pmImporter{
		res:       &Result{},
		authNames: map[string]string{},
		envLits:   map[string]string{},
		warned:    map[string]bool{},
		captured:  map[string]bool{},
	}
	imp.prescanTransfers(col.Event, col.Item)

	name := strings.TrimSpace(col.Info.Name)
	if name == "" {
		name = "Imported Postman Collection"
	}
	project := &model.Project{
		SchemaVersion: "1.0",
		Name:          name,
		Description:   fmt.Sprintf("Generated from Postman collection %s", path),
	}

	colVars := map[string]string{}
	for _, v := range col.Variable {
		if v.Disabled || v.Key == "" {
			continue
		}
		colVars[v.Key] = imp.varValue("collection", v.Key, v.value(), v.Type)
	}
	if len(envPaths) == 0 {
		project.Environments = []model.Environment{{Name: "default", Default: true, Variables: colVars}}
	}
	for i, ep := range envPaths {
		env, err := imp.readEnvironment(ep, colVars)
		if err != nil {
			return nil, err
		}
		env.Default = i == 0
		project.Environments = append(project.Environments, env)
	}
	project.Environments = dedupeEnvNames(project.Environments)

	rootScope := imp.scope(pmScope{}, "collection", col.Auth, col.Event)
	var rootReqs []pmItem
	suiteNames := map[string]int{}
	for _, it := range col.Item {
		if !it.isFolder() {
			rootReqs = append(rootReqs, it)
			continue
		}
		cases := imp.folderCases(it, []string{it.Name}, rootScope)
		if len(cases) == 0 {
			imp.warn(fmt.Sprintf("%s: folder has no requests; skipped", it.Name))
			continue
		}
		project.Suites = append(project.Suites, model.Suite{Name: uniqueName(suiteNames, it.Name), Cases: uniqueCaseNames(cases)})
	}
	if len(rootReqs) > 0 {
		c := imp.buildCase(name, nil, rootReqs, rootScope)
		root := model.Suite{Name: uniqueName(suiteNames, name), Cases: []model.TestCase{c}}
		project.Suites = append([]model.Suite{root}, project.Suites...)
	}
	if len(project.Suites) == 0 {
		return nil, verr.New(verr.CodeImport, "Postman collection contained no requests")
	}

	project.Auth = imp.auths
	imp.promoteCrossCaseCaptures(project)
	project.Mocks = imp.mocks
	imp.res.Project = project
	return imp.res, nil
}

func readPostmanCollection(path string) (*pmCollection, error) {
	data, err := readImportFile(path, "Postman collection", importMaxBytes)
	if err != nil {
		return nil, err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, verr.Wrap(verr.CodeImport, err, "parse Postman collection JSON")
	}
	// Collections fetched from the Postman API are wrapped in {"collection": ...}.
	if inner, ok := probe["collection"]; ok && probe["info"] == nil {
		data = inner
		probe = nil
		_ = json.Unmarshal(data, &probe)
	}
	if probe["requests"] != nil && probe["item"] == nil {
		return nil, verr.New(verr.CodeImport,
			"Postman Collection v1 is not supported; re-export the collection as v2.1")
	}
	var col pmCollection
	if err := json.Unmarshal(data, &col); err != nil {
		return nil, verr.Wrap(verr.CodeImport, err, "parse Postman collection")
	}
	if col.Info.Schema == "" && col.Info.Name == "" {
		return nil, verr.New(verr.CodeImport, "not a Postman collection (missing info)")
	}
	return &col, nil
}

func (imp *pmImporter) readEnvironment(path string, colVars map[string]string) (model.Environment, error) {
	data, err := readImportFile(path, "Postman environment", importMaxBytes)
	if err != nil {
		return model.Environment{}, err
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal(data, &probe) == nil && probe["environment"] != nil && probe["values"] == nil {
		data = probe["environment"]
	}
	var pe pmEnvironment
	if err := json.Unmarshal(data, &pe); err != nil {
		return model.Environment{}, verr.Wrap(verr.CodeImport, err, "parse Postman environment "+path)
	}
	env := model.Environment{Name: strings.TrimSpace(pe.Name), Variables: map[string]string{}}
	if env.Name == "" {
		env.Name = "environment"
	}
	for k, v := range colVars {
		env.Variables[k] = v
	}
	for _, v := range pe.Values {
		if v.Key == "" || (v.Enabled != nil && !*v.Enabled) {
			continue
		}
		env.Variables[v.Key] = imp.varValue("environment "+env.Name, v.Key, rawString(v.Value), v.Type)
	}
	return env, nil
}

// pmImporter carries state shared across the whole conversion.
type pmImporter struct {
	res       *Result
	auths     []model.AuthDef
	authNames map[string]string // canonical auth config -> AuthDef name
	envLits   map[string]string // generated ${env:NAME} -> the literal it replaced
	warned    map[string]bool
	captured  map[string]bool // variables set by any test script in the collection
	mocks     []model.Mock
}

func (imp *pmImporter) warn(msg string) {
	if imp.warned[msg] {
		return
	}
	imp.warned[msg] = true
	imp.res.Warnings = append(imp.res.Warnings, msg)
}

// pmScope is what a folder passes down to its requests: the effective auth
// (Postman resolves request -> folder -> collection) and the assertions and
// transfers converted from collection/folder test scripts, which Postman runs
// for every request in scope.
type pmScope struct {
	auth       *pmAuth
	assertions []model.Assertion
	transfers  []model.TransferSpec
}

// scope derives a child scope, converting the scope's own scripts once.
func (imp *pmImporter) scope(parent pmScope, where string, auth *pmAuth, events []pmEvent) pmScope {
	s := parent
	if auth != nil && auth.Type != "inherit" {
		s.auth = auth
	}
	for _, ev := range events {
		if ev.Disabled {
			continue
		}
		lines := ev.lines()
		switch ev.Listen {
		case "prerequest":
			if n := scriptLines(lines); n > 0 {
				imp.warn(fmt.Sprintf("%s: pre-request script not converted (%d line(s))", where, n))
			}
		case "test":
			r := convertTestScript(lines)
			s.assertions = append(append([]model.Assertion{}, s.assertions...), r.Assertions...)
			s.transfers = append(append([]model.TransferSpec{}, s.transfers...), r.Transfers...)
			imp.warnUnconverted(where, r.Unconverted)
		}
	}
	return s
}

func (imp *pmImporter) warnUnconverted(where string, lines []string) {
	if len(lines) == 0 {
		return
	}
	first := maskLiterals(lines[0])
	if len(first) > 80 {
		first = first[:77] + "..."
	}
	imp.warn(fmt.Sprintf("%s: %d test-script line(s) not converted (first: %s)", where, len(lines), first))
}

// maskLiterals hides the contents of every string literal after the first
// in a script line, so a warning never echoes a hard-coded credential
// (pm.environment.set("token", "…") keeps the variable name only).
func maskLiterals(line string) string {
	var b strings.Builder
	seen := 0
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c != '"' && c != '\'' && c != '`' {
			b.WriteByte(c)
			continue
		}
		end := i + 1
		for end < len(line) && line[end] != c {
			if line[end] == '\\' {
				end++
			}
			end++
		}
		if end >= len(line) {
			b.WriteString(line[i:])
			break
		}
		seen++
		if seen == 1 {
			b.WriteString(line[i : end+1])
		} else {
			b.WriteByte(c)
			b.WriteString("…")
			b.WriteByte(c)
		}
		i = end
	}
	return b.String()
}

// prescanTransfers records every variable any test script captures, so auth
// that depends on a captured value can be emitted as per-step headers (auth
// definitions are resolved once per run, before any transfer happens).
func (imp *pmImporter) prescanTransfers(events []pmEvent, items []pmItem) {
	for _, ev := range events {
		if ev.Listen == "test" && !ev.Disabled {
			for _, t := range convertTestScript(ev.lines()).Transfers {
				imp.captured[t.Name] = true
			}
		}
	}
	for _, it := range items {
		imp.prescanTransfers(it.Event, it.Item)
	}
}

// folderCases returns one case for the folder's direct requests followed by
// the cases of its sub-folders (depth-first, collection order).
func (imp *pmImporter) folderCases(folder pmItem, path []string, parent pmScope) []model.TestCase {
	sc := imp.scope(parent, strings.Join(path, "/"), folder.Auth, folder.Event)
	var reqs []pmItem
	var subs []pmItem
	for _, it := range folder.Item {
		if it.isFolder() {
			subs = append(subs, it)
		} else {
			reqs = append(reqs, it)
		}
	}
	var cases []model.TestCase
	if len(reqs) > 0 {
		cases = append(cases, imp.buildCase(strings.Join(path, " / "), path, reqs, sc))
	}
	for _, sub := range subs {
		cases = append(cases, imp.folderCases(sub, append(append([]string{}, path...), sub.Name), sc)...)
	}
	return cases
}

func (imp *pmImporter) buildCase(name string, path []string, reqs []pmItem, sc pmScope) model.TestCase {
	c := model.TestCase{Name: name}
	where := strings.Join(path, "/")
	if where == "" {
		where = name
	}
	caseAuth := imp.applyAuth(sc.auth, where)
	if caseAuth.ref != "" {
		c.Auth = &model.AuthRef{Use: []string{caseAuth.ref}}
	}
	for _, it := range reqs {
		c.Steps = append(c.Steps, imp.buildStep(it, append(append([]string{}, path...), it.Name), sc, caseAuth))
	}
	return c
}

// authApply is how an effective Postman auth lands on a step: a reference to
// an AuthDef, or headers/query params when the credential is captured at run
// time by a transfer.
type authApply struct {
	ref     string
	headers map[string]string
	query   map[string]string
	none    bool
}

func (a authApply) equal(b authApply) bool {
	return a.ref == b.ref && a.none == b.none &&
		fmt.Sprint(a.headers) == fmt.Sprint(b.headers) && fmt.Sprint(a.query) == fmt.Sprint(b.query)
}

func (imp *pmImporter) buildStep(it pmItem, path []string, sc pmScope, caseAuth authApply) model.Step {
	where := strings.Join(path, "/")
	req := imp.parseRequest(it.Request, where)
	step := model.Step{Name: it.Name, Protocol: "rest"}
	if step.Name == "" {
		step.Name = req.Method + " " + rawURLString(req.URL)
	}

	url, pathVars := imp.buildURL(req.URL, where)
	headers := imp.headers(req.Header, where)
	request := map[string]any{"method": strings.ToUpper(req.Method), "url": url}

	if req.Body != nil && !req.Body.Disabled {
		imp.applyBody(&step, request, headers, req.Body, where)
	}

	// Auth: the request's own auth overrides the folder's effective auth.
	eff := caseAuth
	if req.Auth != nil && req.Auth.Type != "inherit" {
		eff = imp.applyAuth(req.Auth, where)
		if !eff.equal(caseAuth) {
			if eff.ref != "" {
				step.Auth = &model.AuthRef{Use: []string{eff.ref}, Inherit: boolPtr(false)}
			} else if caseAuth.ref != "" {
				step.Auth = &model.AuthRef{Inherit: boolPtr(false)}
			}
		}
	}
	for k, v := range eff.headers {
		if !hasHeader(headers, k) {
			headers[k] = v
		}
	}
	if len(eff.query) > 0 {
		if step.Protocol == "graphql" {
			// request["query"] holds the GraphQL document; put the key in the URL.
			sep := "?"
			if strings.Contains(url, "?") {
				sep = "&"
			}
			for k, v := range eff.query {
				url += sep + k + "=" + v
				sep = "&"
			}
			request["url"] = url
		} else {
			request["query"] = toAnyMap(eff.query)
		}
	}

	if len(headers) > 0 {
		request["headers"] = toAnyMap(headers)
	}
	if step.Protocol == "graphql" {
		delete(request, "method")
	}
	step.Request = request
	if len(pathVars) > 0 {
		step.Vars = pathVars
	}

	// Test scripts: inherited (collection/folder) first, then the request's own.
	own := imp.scope(pmScope{}, where, nil, it.Event)
	step.Assertions = append(append([]model.Assertion{}, sc.assertions...), own.assertions...)
	step.Transfers = append(append([]model.TransferSpec{}, sc.transfers...), own.transfers...)
	if len(step.Assertions) == 0 {
		step.Assertions = []model.Assertion{{Type: "status", Op: "lt", Value: 400, Description: "starter assertion (no Postman tests converted)"}}
	}
	imp.addExampleMocks(it, req, where)
	return step
}

func (imp *pmImporter) parseRequest(raw json.RawMessage, where string) pmRequest {
	var req pmRequest
	var s string
	if json.Unmarshal(raw, &s) == nil {
		req.URL, _ = json.Marshal(s)
		req.Method = "GET"
		return req
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		imp.warn(fmt.Sprintf("%s: request could not be parsed (%v)", where, err))
	}
	if req.Method == "" {
		req.Method = "GET"
	}
	return req
}

func rawURLString(raw json.RawMessage) string {
	var u pmURL
	if json.Unmarshal(raw, &u) == nil && u.Raw != "" {
		return u.Raw
	}
	return rawString(raw)
}

// buildURL returns the Verity URL template plus path-variable values.
func (imp *pmImporter) buildURL(raw json.RawMessage, where string) (string, map[string]string) {
	var u pmURL
	if s := rawString(raw); len(raw) > 0 && raw[0] == '"' {
		u.Raw = s
	} else if len(raw) > 0 && json.Unmarshal(raw, &u) != nil {
		imp.warn(fmt.Sprintf("%s: URL could not be parsed", where))
	}
	full := u.Raw
	if full == "" {
		full = strings.Join(rawStrings(u.Host), ".")
		if u.Protocol != "" {
			full = u.Protocol + "://" + full
		}
		if u.Port != "" {
			full += ":" + u.Port
		}
		if p := rawStrings(u.Path); len(p) > 0 {
			full += "/" + strings.Join(p, "/")
		}
		var qs []string
		for _, q := range u.Query {
			if !q.Disabled {
				qs = append(qs, q.Key+"="+q.value())
			}
		}
		if len(qs) > 0 {
			full += "?" + strings.Join(qs, "&")
		}
	}
	if full == "" {
		imp.warn(fmt.Sprintf("%s: request has no URL", where))
	}

	// Work on the query string itself (not url.query metadata, which may be
	// missing): drop parameters Postman marks disabled, and move literal
	// credentials to env references.
	base, query, hasQuery := strings.Cut(full, "?")
	if hasQuery {
		disabled := map[string]bool{}
		for _, q := range u.Query {
			if q.Disabled {
				disabled[q.Key+"="+q.value()] = true
			}
		}
		var kept []string
		for _, pair := range strings.Split(query, "&") {
			if disabled[pair] {
				continue
			}
			k, v, _ := strings.Cut(pair, "=")
			if hasLiteral(v) && isSecretKey(k) {
				env := imp.envRef(k, v)
				pair = k + "=" + env
				imp.warn(fmt.Sprintf("%s: query parameter %q held a literal credential; set environment variable %s", where, k, envName(env)))
			}
			kept = append(kept, pair)
		}
		query = strings.Join(kept, "&")
		hasQuery = query != ""
	}

	// Path variables (:id) become ${id}; only the path part is rewritten.
	base = rePathVar.ReplaceAllString(base, "/$${$1}")
	if hasQuery {
		full = base + "?" + query
	} else {
		full = base
	}
	vars := map[string]string{}
	for _, v := range u.Variable {
		if !v.Disabled && v.Key != "" && v.value() != "" {
			vars[v.Key] = imp.rewriteVars(v.value(), where)
		}
	}
	return imp.rewriteVars(full, where), vars
}

func (imp *pmImporter) headers(raw json.RawMessage, where string) map[string]string {
	var list []pmKV
	if json.Unmarshal(raw, &list) != nil {
		// v2.0 also allows a "Key: value\n" string.
		for _, line := range strings.Split(rawString(raw), "\n") {
			if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) != "" {
				list = append(list, pmKV{Key: strings.TrimSpace(k), Value: mustJSON(strings.TrimSpace(v))})
			}
		}
	}
	out := map[string]string{}
	for _, h := range list {
		if h.Disabled || h.Key == "" {
			continue
		}
		if hasHeader(out, h.Key) {
			imp.warn(fmt.Sprintf("%s: duplicate header %q; kept the last value", where, h.Key))
		}
		out[h.Key] = imp.headerValue(h.Key, h.value(), where)
	}
	return out
}

func (imp *pmImporter) headerValue(name, v, where string) string {
	// Keep an auth scheme ("Bearer ") readable; only the credential moves.
	prefix, cred := "", v
	if scheme, rest, ok := strings.Cut(v, " "); ok && reAuthScheme.MatchString(scheme) && rest != "" {
		prefix, cred = scheme+" ", rest
	}
	if !hasLiteral(cred) || !(reSecretHeader.MatchString(name) || secrets.LooksLikeSecret(cred)) {
		return imp.rewriteVars(v, where)
	}
	env := imp.envRef(name, cred)
	imp.warn(fmt.Sprintf("%s: header %q held a literal credential; set environment variable %s", where, name, envName(env)))
	return prefix + env
}

func (imp *pmImporter) applyBody(step *model.Step, request map[string]any, headers map[string]string, b *pmBody, where string) {
	switch b.Mode {
	case "raw", "":
		if b.Raw == "" {
			return
		}
		request["body"] = imp.rewriteVars(imp.scrubBody(b.Raw, where), where)
		if strings.EqualFold(b.Options.Raw.Language, "json") && !hasHeader(headers, "Content-Type") {
			headers["Content-Type"] = "application/json"
		}
	case "urlencoded":
		form := map[string]any{}
		for _, kv := range b.URLEncoded {
			if kv.Disabled || kv.Key == "" {
				continue
			}
			if _, dup := form[kv.Key]; dup {
				imp.warn(fmt.Sprintf("%s: duplicate form field %q; kept the last value", where, kv.Key))
			}
			v := kv.value()
			if hasLiteral(v) && isSecretKey(kv.Key) {
				env := imp.envRef(kv.Key, v)
				imp.warn(fmt.Sprintf("%s: form field %q held a literal credential; set environment variable %s", where, kv.Key, envName(env)))
				form[kv.Key] = env
				continue
			}
			form[kv.Key] = imp.rewriteVars(v, where)
		}
		if len(form) > 0 {
			request["form"] = form
		}
	case "graphql":
		if b.GraphQL == nil {
			return
		}
		query := imp.rewriteVars(b.GraphQL.Query, where)
		varsSrc := strings.TrimSpace(imp.rewriteVars(imp.scrubBody(b.GraphQL.Variables, where), where))
		var vars map[string]any
		if varsSrc == "" || json.Unmarshal([]byte(varsSrc), &vars) == nil {
			step.Protocol = "graphql"
			request["query"] = query
			if len(vars) > 0 {
				request["variables"] = vars
			}
			return
		}
		// Variables that are not valid JSON on their own (e.g. an unquoted
		// {{id}}) are sent as a hand-built JSON body over REST instead.
		q, _ := json.Marshal(query)
		request["method"] = "POST"
		request["body"] = fmt.Sprintf(`{"query":%s,"variables":%s}`, q, varsSrc)
		if !hasHeader(headers, "Content-Type") {
			headers["Content-Type"] = "application/json"
		}
		imp.warn(fmt.Sprintf("%s: GraphQL variables are not plain JSON; sent as a raw REST body", where))
	case "formdata":
		parts, dropped := imp.formDataParts(b.FormData, where)
		if len(parts) > 0 {
			request["multipart"] = parts
			return
		}
		if dropped {
			step.Skip = true
			imp.warn(fmt.Sprintf("%s: formdata body has no usable parts; step imported with skip: true", where))
		}
	case "file":
		src := ""
		if b.File != nil {
			src = strings.TrimSpace(b.File.Src)
		}
		if src == "" {
			step.Skip = true
			imp.warn(fmt.Sprintf("%s: file body has no src; step imported with skip: true", where))
			return
		}
		request["bodyFile"] = imp.rewriteVars(src, where)
	default:
		imp.warn(fmt.Sprintf("%s: unknown body mode %q ignored", where, b.Mode))
	}
}

// formDataParts converts Postman form-data rows into multipart parts.
// Disabled rows are omitted. dropped is true when a row was skipped because
// it had no key or a file row had no src.
func (imp *pmImporter) formDataParts(rows []pmKV, where string) (parts []any, dropped bool) {
	for _, kv := range rows {
		if kv.Disabled {
			continue
		}
		if kv.Key == "" {
			dropped = true
			imp.warn(fmt.Sprintf("%s: formdata part has no key; skipped", where))
			continue
		}
		name := imp.rewriteVars(kv.Key, where)
		if !strings.EqualFold(kv.Type, "file") {
			parts = append(parts, map[string]any{"name": name, "value": imp.formFieldValue(where, kv.Key, kv.value())})
			continue
		}
		var sources []string
		for _, src := range rawStrings(kv.Src) {
			if strings.TrimSpace(src) != "" {
				sources = append(sources, strings.TrimSpace(src))
			}
		}
		if len(sources) == 0 {
			dropped = true
			imp.warn(fmt.Sprintf("%s: formdata file part %q has no src", where, kv.Key))
			continue
		}
		for _, src := range sources {
			part := map[string]any{"name": name, "file": imp.rewriteVars(src, where)}
			if ct := strings.TrimSpace(kv.ContentType); ct != "" {
				part["contentType"] = imp.rewriteVars(ct, where)
			}
			parts = append(parts, part)
		}
	}
	return parts, dropped
}

// formFieldValue rewrites a form-data text value the same way url-encoded
// fields are rewritten, and scrubs credential-shaped JSON inside the value.
func (imp *pmImporter) formFieldValue(where, key, v string) string {
	if hasLiteral(v) && (isSecretKey(key) || secrets.LooksLikeSecret(v)) {
		env := imp.envRef(key, v)
		imp.warn(fmt.Sprintf("%s: formdata field %q held a literal credential; set environment variable %s", where, key, envName(env)))
		return env
	}
	return imp.rewriteVars(imp.scrubBody(v, where), where)
}

// scrubBody replaces literal values of credential-named JSON fields
// ("password": "hunter2") with ${env:NAME} references.
func (imp *pmImporter) scrubBody(body, where string) string {
	return reBodySecret.ReplaceAllStringFunc(body, func(m string) string {
		sm := reBodySecret.FindStringSubmatch(m)
		env := imp.envRef(sm[1], sm[2])
		imp.warn(fmt.Sprintf("%s: body field %q held a literal credential; set environment variable %s", where, sm[1], envName(env)))
		return strings.Replace(m, `"`+sm[2]+`"`, `"`+env+`"`, 1)
	})
}

// applyAuth converts a Postman auth block (memoised by content) into how it
// is applied to a step.
func (imp *pmImporter) applyAuth(a *pmAuth, where string) authApply {
	if a == nil {
		return authApply{}
	}
	p := func(k string) string { return a.Params[k] }
	switch a.Type {
	case "noauth", "":
		return authApply{none: true}
	case "bearer":
		if v := p("token"); imp.isCaptured(v) {
			return authApply{headers: map[string]string{"Authorization": "Bearer " + imp.rewriteVars(v, where)}}
		}
		return authApply{ref: imp.authDef(a, "bearer", where, func(name string) map[string]any {
			return map[string]any{"token": imp.credential(name, "token", p("token"), where)}
		})}
	case "basic", "digest":
		if imp.isCaptured(p("username")) || imp.isCaptured(p("password")) {
			imp.warn(fmt.Sprintf("%s: %s auth uses a variable captured at run time; auth definitions resolve before captures, so set it in the environment instead", where, a.Type))
		}
		return authApply{ref: imp.authDef(a, a.Type, where, func(name string) map[string]any {
			cfg := map[string]any{
				"username": imp.rewriteVars(p("username"), where),
				"password": imp.credential(name, "password", p("password"), where),
			}
			if a.Type == "digest" && p("algorithm") != "" {
				cfg["algorithm"] = p("algorithm")
			}
			return cfg
		})}
	case "apikey":
		in := strings.ToLower(p("in"))
		if in == "" {
			in = "header"
		}
		if in != "header" && in != "query" {
			imp.warn(fmt.Sprintf("%s: API key location %q not supported; auth not converted", where, in))
			return authApply{}
		}
		key := p("key")
		if key == "" {
			key = "X-API-Key"
		}
		if v := p("value"); imp.isCaptured(v) {
			kv := map[string]string{key: imp.rewriteVars(v, where)}
			if in == "query" {
				return authApply{query: kv}
			}
			return authApply{headers: kv}
		}
		return authApply{ref: imp.authDef(a, "apikey", where, func(name string) map[string]any {
			return map[string]any{
				"name":  imp.rewriteVars(key, where),
				"value": imp.credential(name, "value", p("value"), where),
				"in":    in,
			}
		})}
	case "oauth2":
		grant := p("grant_type")
		if grant == "password_credentials" {
			grant = "password"
		}
		if grant == "authorization_code_with_pkce" || grant == "" {
			grant = "authorization_code"
		}
		if grant == "client_credentials" || grant == "password" || grant == "authorization_code" || grant == "refresh_token" {
			typ := "oauth2"
			if grant == "client_credentials" {
				typ = "oauth2_client_credentials"
			}
			return authApply{ref: imp.authDef(a, typ, where, func(name string) map[string]any {
				cfg := map[string]any{
					"tokenUrl": imp.rewriteVars(p("accessTokenUrl"), where),
					"clientId": imp.rewriteVars(p("clientId"), where),
				}
				if grant != "authorization_code" || p("clientSecret") != "" {
					cfg["clientSecret"] = imp.credential(name, "clientSecret", p("clientSecret"), where)
				}
				if grant != "client_credentials" {
					cfg["grant"] = grant
				}
				if grant == "authorization_code" {
					cfg["authUrl"] = imp.rewriteVars(p("authUrl"), where)
				}
				if grant == "authorization_code" || grant == "refresh_token" {
					cfg["refreshToken"] = imp.credential(name, "refreshToken", p("refreshToken"), where)
				}
				if grant == "password" {
					cfg["username"] = imp.rewriteVars(p("username"), where)
					cfg["password"] = imp.credential(name, "password", p("password"), where)
				}
				if s := p("scope"); s != "" {
					cfg["scope"] = imp.rewriteVars(s, where)
				}
				if p("client_authentication") == "body" {
					cfg["authStyle"] = "body"
				}
				return cfg
			})}
		}
		imp.warn(fmt.Sprintf("%s: OAuth2 %q grant is not supported yet; imported as a bearer token you must supply", where, grant))
		if v := p("accessToken"); imp.isCaptured(v) {
			return authApply{headers: map[string]string{"Authorization": "Bearer " + imp.rewriteVars(v, where)}}
		}
		return authApply{ref: imp.authDef(a, "bearer", where, func(name string) map[string]any {
			return map[string]any{"token": imp.credential(name, "token", p("accessToken"), where)}
		})}
	default:
		imp.warn(fmt.Sprintf("%s: %s auth is not supported; requests will be sent without it", where, a.Type))
		return authApply{none: true}
	}
}

// authDef returns the AuthDef name for a, creating it on first sight.
func (imp *pmImporter) authDef(a *pmAuth, typ, where string, config func(name string) map[string]any) string {
	key := canonicalAuth(a)
	if name, ok := imp.authNames[key]; ok {
		return name
	}
	base := strings.ReplaceAll(typ, "_", "-")
	name := base
	for i := 2; imp.hasAuth(name); i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	imp.auths = append(imp.auths, model.AuthDef{Name: name, Type: typ, Config: config(name)})
	imp.authNames[key] = name
	return name
}

func (imp *pmImporter) hasAuth(name string) bool {
	for _, d := range imp.auths {
		if d.Name == name {
			return true
		}
	}
	return false
}

func canonicalAuth(a *pmAuth) string {
	keys := make([]string, 0, len(a.Params))
	for k := range a.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(a.Type)
	for _, k := range keys {
		b.WriteString("\x00" + k + "=" + a.Params[k])
	}
	return b.String()
}

// credential keeps template values as ${var} references and moves literal
// (or missing) credentials to ${env:NAME}, warning which variable to set.
func (imp *pmImporter) credential(authName, field, v, where string) string {
	if v != "" && !hasLiteral(v) {
		return imp.rewriteVars(v, where)
	}
	env := imp.envRef(authName+"_"+field, v)
	if v == "" {
		imp.warn(fmt.Sprintf("auth %q: %s is empty in the collection; set environment variable %s", authName, field, envName(env)))
	} else {
		imp.warn(fmt.Sprintf("auth %q: literal %s not imported; set environment variable %s", authName, field, envName(env)))
	}
	return env
}

// varValue converts a collection/environment variable value, replacing
// secret-looking literals with ${env:NAME}.
func (imp *pmImporter) varValue(where, key, v, typ string) string {
	if v == "" || rePMPureVar.MatchString(v) {
		return imp.rewriteVars(v, where)
	}
	secret := typ == "secret" ||
		(isSecretKey(key) && hasLiteral(v)) ||
		(hasLiteral(v) && secrets.LooksLikeSecret(v))
	if !secret {
		return imp.rewriteVars(v, where)
	}
	env := imp.envRef(key, v)
	imp.warn(fmt.Sprintf("%s variable %q: secret value not imported; set environment variable %s", where, key, envName(env)))
	return env
}

// envRef returns a ${env:NAME} reference for a literal, suffixing NAME when a
// different literal already claimed it.
func (imp *pmImporter) envRef(base, literal string) string {
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

func envName(ref string) string {
	return strings.TrimSuffix(strings.TrimPrefix(ref, "${env:"), "}")
}

// rewriteVars converts {{var}} to ${var} and supported Postman dynamic
// variables to interpolation functions.
func (imp *pmImporter) rewriteVars(s, where string) string {
	return rePMVar.ReplaceAllStringFunc(s, func(m string) string {
		name := rePMVar.FindStringSubmatch(m)[1]
		if strings.HasPrefix(name, "$") {
			switch name {
			case "$guid", "$randomUUID":
				return "${fn:uuid}"
			case "$timestamp":
				return "${fn:timestamp}"
			case "$isoTimestamp":
				return "${fn:isoTimestamp}"
			case "$randomInt":
				return "${fn:randomInt}"
			default:
				imp.warn(fmt.Sprintf("dynamic variable {{%s}} has no Verity equivalent; left as-is (first seen in %s)", name, where))
				return m
			}
		}
		return "${" + name + "}"
	})
}

// isCaptured reports whether v is exactly {{var}} for a variable that a test
// script captures at run time.
func (imp *pmImporter) isCaptured(v string) bool {
	m := rePMPureVar.FindStringSubmatch(strings.TrimSpace(v))
	return m != nil && imp.captured[m[1]]
}

// promoteCrossCaseCaptures gives transfers run scope when a later case uses
// the value: Postman variables are global to the run, Verity transfers are
// case-scoped unless marked scope: run. A case that uses a value captured only
// by a case that runs after it is warned about instead.
func (imp *pmImporter) promoteCrossCaseCaptures(p *model.Project) {
	type capture struct {
		order int
		where string
		t     *model.TransferSpec
	}
	captures := map[string][]capture{}
	order := 0
	for si := range p.Suites {
		s := &p.Suites[si]
		for ci := range s.Cases {
			c := &s.Cases[ci]
			for sti := range c.Steps {
				for ti := range c.Steps[sti].Transfers {
					t := &c.Steps[sti].Transfers[ti]
					captures[t.Name] = append(captures[t.Name], capture{order, s.Name + "/" + c.Name, t})
				}
			}
			order++
		}
	}
	order = 0
	for _, s := range p.Suites {
		for _, c := range s.Cases {
			// own holds names this case has captured so far: a request reads
			// variables before its own transfers run, so a value the case
			// captures only later still has to come from an earlier case.
			own := map[string]bool{}
			warned := map[string]bool{}
			for _, st := range c.Steps {
				b, _ := json.Marshal(st.Request)
				for _, m := range reVerityRef.FindAllStringSubmatch(string(b), -1) {
					name := m[1]
					if own[name] || len(captures[name]) == 0 {
						continue
					}
					earlier := false
					for _, cp := range captures[name] {
						if cp.order < order {
							cp.t.Scope = model.TransferScopeRun
							earlier = true
						}
					}
					if !earlier && !warned[name] {
						warned[name] = true
						imp.warn(fmt.Sprintf("%s/%s: uses ${%s}, which is only captured later, in %s; move that request earlier or set %s in the environment",
							s.Name, c.Name, name, captures[name][0].where, name))
					}
				}
				for _, t := range st.Transfers {
					own[t.Name] = true
				}
			}
			order++
		}
	}
}

// hasLiteral reports whether v has any text outside {{template}} references.
// "hunter2{{SUFFIX}}" does: a secret-named field holding it is a literal
// credential, not a reference, and must not reach the project as-is.
func hasLiteral(v string) bool {
	return strings.TrimSpace(rePMVar.ReplaceAllString(v, "")) != ""
}

func isSecretKey(k string) bool {
	return reSecretKey.MatchString(k) && !reNotSecretKey.MatchString(k)
}

func hasHeader(h map[string]string, name string) bool {
	for k := range h {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

func toAnyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func mustJSON(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func boolPtr(b bool) *bool { return &b }

// uniqueCaseNames renames duplicate case names within a suite to "name (2)"
// and so on: a case name is its identity, and duplicates fail validation.
func uniqueCaseNames(cases []model.TestCase) []model.TestCase {
	used := map[string]int{}
	for i := range cases {
		cases[i].Name = uniqueName(used, cases[i].Name)
	}
	return cases
}

// uniqueName returns base, or base (n) when base was already used.
func uniqueName(used map[string]int, base string) string {
	if strings.TrimSpace(base) == "" {
		base = "Unnamed"
	}
	used[base]++
	if used[base] == 1 {
		return base
	}
	// Skip suffixes another item already took verbatim ("A (2)" as a name).
	for i := used[base]; ; i++ {
		name := fmt.Sprintf("%s (%d)", base, i)
		if used[name] == 0 {
			used[name]++
			return name
		}
	}
}

// addExampleMocks turns saved example responses into project mocks. A path
// segment that is only a Postman variable becomes a {name} template. Literal
// credentials in the example body or headers become the placeholder REDACTED.
func (imp *pmImporter) addExampleMocks(it pmItem, parent pmRequest, where string) {
	for i, ex := range it.Response {
		req := parent
		if len(ex.OriginalRequest) > 0 {
			req = imp.parseRequest(ex.OriginalRequest, where)
		}
		path := exampleMockPath(req.URL)
		if path == "" {
			imp.warn(fmt.Sprintf("%s: saved example %q has no request path; skipped", where, exampleName(ex, i)))
			continue
		}
		path, pathFields := scrubMockPath(path)
		headers := rawHeaderMap(ex.Header)
		body := rawString(ex.Body)
		headers, body, fields := imp.scrubExample(where, headers, body)
		fields = append(fields, pathFields...)
		if len(fields) > 0 {
			sort.Strings(fields)
			// The example's own name is not printed: it is not stored on the
			// mock, and it may itself be a credential.
			imp.warn(fmt.Sprintf("%s: saved example contained a literal credential (%s); replaced with REDACTED", where, strings.Join(fields, ", ")))
		}
		status := ex.Code
		if status == 0 {
			status = 200
		}
		method := req.Method
		if method == "" {
			method = "GET"
		}
		mock := model.Mock{
			Method:  strings.ToUpper(method),
			Path:    path,
			Status:  status,
			Headers: headers,
			Body:    body,
		}
		imp.mocks = append(imp.mocks, mock)
	}
}

func exampleName(ex pmExample, i int) string {
	if strings.TrimSpace(ex.Name) != "" {
		return ex.Name
	}
	return fmt.Sprintf("response %d", i+1)
}

func (imp *pmImporter) scrubExample(where string, headers map[string]string, body string) (map[string]string, string, []string) {
	body = imp.rewriteVars(body, where)
	var fields []string
	for k, v := range headers {
		rewritten := imp.rewriteVars(v, where)
		if len(secretscan.ScanString([]string{"headers", k}, k, rewritten)) > 0 {
			headers[k] = "REDACTED"
			fields = append(fields, k)
			continue
		}
		headers[k] = rewritten
	}
	scrubbed, bodyFields := scrubExampleBody(body)
	return headers, scrubbed, append(fields, bodyFields...)
}

func scrubExampleBody(body string) (string, []string) {
	text := strings.TrimSpace(body)
	if text == "" {
		return body, nil
	}
	if text[0] == '{' || text[0] == '[' {
		var v any
		if json.Unmarshal([]byte(text), &v) == nil {
			nv, fields := scrubJSON(v)
			if len(fields) == 0 {
				return body, nil
			}
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			if enc.Encode(nv) != nil {
				return "REDACTED", []string{"body"}
			}
			return strings.TrimRight(buf.String(), "\n"), fields
		}
	}
	if secretscan.Sensitive("body", body, false) {
		return "REDACTED", []string{"body"}
	}
	return body, nil
}

func scrubJSON(v any) (any, []string) {
	return scrubJSONKey("", v, []string{"body"})
}

func scrubJSONKey(key string, v any, path []string) (any, []string) {
	switch t := v.(type) {
	case map[string]any:
		var fields []string
		base := path
		if key != "" {
			base = append(append([]string{}, path...), key)
		}
		for k, child := range t {
			nv, f := scrubJSONKey(k, child, base)
			t[k] = nv
			fields = append(fields, f...)
		}
		return t, fields
	case []any:
		var fields []string
		base := path
		if key != "" {
			base = append(append([]string{}, path...), key)
		}
		for i, child := range t {
			nv, f := scrubJSONKey(key, child, base)
			t[i] = nv
			fields = append(fields, f...)
		}
		return t, fields
	case string:
		if secretAt(path, key, t) {
			label := key
			if label == "" {
				label = "body"
			}
			return "REDACTED", []string{label}
		}
		return t, nil
	default:
		if v == nil {
			return v, nil
		}
		if secretAt(path, key, fmt.Sprint(v)) {
			label := key
			if label == "" {
				label = "body"
			}
			return "REDACTED", []string{label}
		}
		return v, nil
	}
}

func secretAt(path []string, key, value string) bool {
	scanPath := path
	if key != "" {
		scanPath = append(append([]string{}, path...), key)
	}
	return len(secretscan.ScanString(scanPath, key, value)) > 0
}

func rawHeaderMap(raw json.RawMessage) map[string]string {
	var list []pmKV
	if json.Unmarshal(raw, &list) != nil {
		for _, line := range strings.Split(rawString(raw), "\n") {
			if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) != "" {
				list = append(list, pmKV{Key: strings.TrimSpace(k), Value: mustJSON(strings.TrimSpace(v))})
			}
		}
	}
	out := map[string]string{}
	for _, h := range list {
		if h.Disabled || h.Key == "" {
			continue
		}
		out[h.Key] = h.value()
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// scrubMockPath replaces a path segment the secret scanner would flag.
// Templates such as {id} are left alone.
func scrubMockPath(path string) (string, []string) {
	raw := strings.Trim(path, "/")
	if raw == "" {
		return path, nil
	}
	var parts, fields []string
	for _, seg := range strings.Split(raw, "/") {
		if seg == "" {
			continue
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") && !strings.ContainsAny(seg[1:len(seg)-1], "{}") {
			parts = append(parts, seg)
			continue
		}
		if len(secretscan.ScanString([]string{"mocks", "0", "path"}, "path", seg)) > 0 {
			parts = append(parts, "REDACTED")
			fields = append(fields, "path")
			continue
		}
		parts = append(parts, seg)
	}
	if len(parts) == 0 {
		return "/", fields
	}
	return "/" + strings.Join(parts, "/"), fields
}

func exampleMockPath(raw json.RawMessage) string {
	var u pmURL
	if len(raw) > 0 && raw[0] == '"' {
		u.Raw = rawString(raw)
	} else if len(raw) > 0 {
		_ = json.Unmarshal(raw, &u)
	}
	if segs := rawStrings(u.Path); len(segs) > 0 {
		return joinMockPath(segs)
	}
	return mockPathFromRaw(u.Raw)
}

func joinMockPath(segs []string) string {
	var parts []string
	for _, s := range segs {
		s = strings.Trim(s, "/")
		if s == "" {
			continue
		}
		parts = append(parts, mockSegment(s))
	}
	if len(parts) == 0 {
		return "/"
	}
	return "/" + strings.Join(parts, "/")
}

func mockSegment(s string) string {
	s = strings.TrimSpace(s)
	if m := rePMPureVar.FindStringSubmatch(s); m != nil {
		name := strings.TrimPrefix(strings.TrimSpace(m[1]), "$")
		if name == "" {
			return s
		}
		return "{" + name + "}"
	}
	if strings.HasPrefix(s, ":") && len(s) > 1 && !strings.ContainsAny(s[1:], "/?#") {
		return "{" + s[1:] + "}"
	}
	return s
}

func mockPathFromRaw(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	if raw == "" {
		return ""
	}
	if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			raw = rest[j:]
		} else {
			return "/"
		}
	} else if !strings.HasPrefix(raw, "/") {
		bits := strings.Split(raw, "/")
		if len(bits) > 0 && rePMPureVar.MatchString(strings.TrimSpace(bits[0])) {
			bits = bits[1:]
		}
		raw = "/" + strings.Join(bits, "/")
	}
	raw = rePathVar.ReplaceAllString(raw, "/{$1}")
	return joinMockPath(strings.Split(strings.Trim(raw, "/"), "/"))
}

func dedupeEnvNames(envs []model.Environment) []model.Environment {
	used := map[string]int{}
	for i := range envs {
		envs[i].Name = uniqueName(used, envs[i].Name)
	}
	return envs
}
