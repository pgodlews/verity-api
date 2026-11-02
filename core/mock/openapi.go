package mock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/pgodlews/verity-api/core/verr"
)

type oasServer struct {
	root   map[string]any
	routes []oasRoute
	opts   Options
}

type oasRoute struct {
	method string
	segs   []seg
	spec   int
	index  int
	op     *oasOp
}

type oasOp struct {
	params       []oasParam
	bodyRequired bool
	bodies       []oasBody
	responses    []oasResp
}

type oasParam struct {
	name     string
	in       string
	required bool
	schema   any
	style    string
	explode  *bool
}

type oasBody struct {
	contentType string
	schema      any
}

type oasResp struct {
	status int // exact code, or 0 for default and for ranges
	class  int // 2 for "2XX", 0 when this is not a range
	medias []oasMedia
}

type oasMedia struct {
	contentType string
	order       []namedBody
	fallback    []byte
}

type namedBody struct {
	name string
	body []byte
}

type yamlPair struct {
	k string
	v *yaml.Node
}

func newOpenAPI(data []byte, opts Options) (http.Handler, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, verr.Wrap(verr.CodeConfig, err, "parse OpenAPI")
	}
	rootNode := docNode(&doc)
	var rootAny any
	if rootNode == nil {
		return nil, verr.New(verr.CodeConfig, "OpenAPI document is empty")
	}
	if err := rootNode.Decode(&rootAny); err != nil {
		return nil, verr.Wrap(verr.CodeConfig, err, "parse OpenAPI")
	}
	root := asMap(rootAny)
	if root == nil {
		return nil, verr.New(verr.CodeConfig, "OpenAPI document is not an object")
	}
	paths := mapGet(rootNode, "paths")
	if paths == nil || paths.Kind != yaml.MappingNode {
		return nil, verr.New(verr.CodeConfig, "OpenAPI document has no paths")
	}
	g := newGen(root, opts.Seed)
	prefix := serverPrefix(root)
	seen := map[string]bool{}
	var routes []oasRoute
	index := 0
	for _, pp := range mapPairs(paths) {
		tmpl := pp.k
		if !strings.HasPrefix(tmpl, "/") {
			continue
		}
		item := resolveNode(rootNode, pp.v)
		pathParams := readParams(rootNode, mapGet(item, "parameters"))
		for _, mp := range mapPairs(item) {
			if !isHTTPMethod(mp.k) {
				continue
			}
			opNode := resolveNode(rootNode, mp.v)
			op := buildOp(rootNode, opNode, pathParams, g, opts.Source)
			method := strings.ToUpper(mp.k)
			for _, path := range []string{tmpl, joinBase(prefix, tmpl)} {
				if path == "" {
					continue
				}
				key := method + " " + path
				if seen[key] {
					continue
				}
				seen[key] = true
				segs := parseTemplate(path)
				routes = append(routes, oasRoute{
					method: method,
					segs:   segs,
					spec:   specificity(segs),
					index:  index,
					op:     op,
				})
				index++
			}
		}
	}
	if len(routes) == 0 {
		return nil, verr.New(verr.CodeConfig, "OpenAPI document has no operations")
	}
	return &oasServer{root: root, routes: routes, opts: opts}, nil
}

func (s *oasServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	pause(r, s.opts.Delay)
	if r.Context().Err() != nil {
		return
	}
	route, params := s.match(r.Method, r.URL.Path)
	if route == nil {
		writeProblem(w, http.StatusNotFound, "Not Found",
			fmt.Sprintf("no operation for %s %s", r.Method, r.URL.Path), nil)
		return
	}
	if s.opts.Validate {
		errs, fail := s.validate(route.op, r, params)
		if fail == http.StatusRequestEntityTooLarge {
			writeProblem(w, fail, "Payload Too Large", errBodyTooLarge.Error(), nil)
			return
		}
		if len(errs) > 0 {
			sort.Strings(errs)
			writeProblem(w, http.StatusBadRequest, "Bad Request",
				"request does not match the OpenAPI document", errs)
			return
		}
	}
	example, code := parsePrefer(r.Header)
	resp, status, ok := pickResponse(route.op.responses, code)
	if !ok {
		writeProblem(w, http.StatusNotFound, "Not Found",
			fmt.Sprintf("no response defined for status %d", code), nil)
		return
	}
	media := pickMedia(resp.medias, r.Header.Get("Accept"))
	if media.contentType != "" {
		w.Header().Set("Content-Type", media.contentType)
	}
	w.WriteHeader(status)
	_, _ = w.Write(media.pick(example))
}

func (s *oasServer) match(method, path string) (*oasRoute, map[string]string) {
	method = strings.ToUpper(method)
	var best *oasRoute
	var params map[string]string
	for i := range s.routes {
		rt := &s.routes[i]
		if rt.method != method {
			continue
		}
		got, ok := matchPath(rt.segs, path)
		if !ok {
			continue
		}
		if best == nil || rt.spec > best.spec || (rt.spec == best.spec && rt.index < best.index) {
			best = rt
			params = got
		}
	}
	return best, params
}

func (m oasMedia) pick(name string) []byte {
	if name != "" {
		for _, b := range m.order {
			if b.name == name {
				return b.body
			}
		}
	}
	if len(m.order) > 0 {
		return m.order[0].body
	}
	if m.fallback != nil {
		return m.fallback
	}
	return []byte("{}")
}

func buildOp(root, op *yaml.Node, pathParams []oasParam, g *gen, specPath string) *oasOp {
	out := &oasOp{
		params:    mergeParams(pathParams, readParams(root, mapGet(op, "parameters"))),
		responses: buildResponses(root, op, g, specPath),
	}
	out.bodyRequired, out.bodies = readBody(root, op)
	return out
}

func buildResponses(root, op *yaml.Node, g *gen, specPath string) []oasResp {
	var out []oasResp
	for _, p := range mapPairs(mapGet(op, "responses")) {
		code, class, ok := statusKey(p.k)
		if !ok {
			continue
		}
		rn := resolveNode(root, p.v)
		var medias []oasMedia
		for _, c := range mapPairs(mapGet(rn, "content")) {
			medias = append(medias, buildMedia(root, c.k, c.v, g, specPath))
		}
		if len(medias) == 0 {
			medias = []oasMedia{{contentType: "application/json", fallback: []byte("{}")}}
		}
		out = append(out, oasResp{status: code, class: class, medias: medias})
	}
	return out
}

func buildMedia(root *yaml.Node, ct string, n *yaml.Node, g *gen, specPath string) oasMedia {
	n = concrete(n)
	m := oasMedia{contentType: ct}
	for _, p := range mapPairs(mapGet(n, "examples")) {
		val, ok := exampleValue(root, p.v, specPath, ct)
		if !ok {
			continue
		}
		m.order = append(m.order, namedBody{name: p.k, body: encodeBody(ct, val)})
	}
	if len(m.order) == 0 {
		if ex := mapGet(n, "example"); ex != nil {
			var v any
			if concrete(ex).Decode(&v) == nil {
				m.order = []namedBody{{body: encodeBody(ct, v)}}
			}
		}
	}
	var schema any
	if sn := mapGet(n, "schema"); sn != nil {
		_ = concrete(sn).Decode(&schema)
	}
	if len(m.order) == 0 {
		if schema != nil {
			m.fallback = encodeBody(ct, g.sample(schema))
		} else if isJSONType(ct) || ct == "" {
			m.fallback = []byte("{}")
			if m.contentType == "" {
				m.contentType = "application/json"
			}
		}
	}
	return m
}

func exampleValue(root, n *yaml.Node, specPath, ct string) (any, bool) {
	n = resolveNode(root, n)
	if n == nil {
		return nil, false
	}
	if v := mapGet(n, "value"); v != nil {
		var out any
		if concrete(v).Decode(&out) != nil {
			return nil, false
		}
		return out, true
	}
	if ev := mapGet(n, "externalValue"); ev != nil {
		ref := nodeString(ev)
		b, err := readExternal(specPath, ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mock: skipping externalValue %q: %s\n", ref, err.Error())
			return nil, false
		}
		out, err := decodeExternal(ct, b)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mock: skipping externalValue %q: %s\n", ref, err.Error())
			return nil, false
		}
		return out, true
	}
	return nil, false
}

func decodeExternal(ct string, b []byte) (any, error) {
	if isJSONType(ct) || ct == "" {
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			return nil, errors.New("externalValue is not JSON")
		}
		return v, nil
	}
	return string(bytes.TrimSpace(b)), nil
}

func readBody(root, op *yaml.Node) (bool, []oasBody) {
	rb := resolveNode(root, mapGet(op, "requestBody"))
	if rb == nil {
		return false, nil
	}
	var bodies []oasBody
	for _, c := range mapPairs(mapGet(rb, "content")) {
		var schema any
		if sn := mapGet(c.v, "schema"); sn != nil {
			_ = concrete(sn).Decode(&schema)
		}
		bodies = append(bodies, oasBody{contentType: c.k, schema: schema})
	}
	return nodeBool(mapGet(rb, "required")), bodies
}

func readParams(root, n *yaml.Node) []oasParam {
	n = concrete(n)
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	var out []oasParam
	for _, item := range n.Content {
		item = resolveNode(root, item)
		name := nodeString(mapGet(item, "name"))
		in := nodeString(mapGet(item, "in"))
		if name == "" || in == "" {
			continue
		}
		var schema any
		if sn := mapGet(item, "schema"); sn != nil {
			_ = concrete(sn).Decode(&schema)
		}
		var explode *bool
		if en := mapGet(item, "explode"); en != nil {
			b := nodeBool(en)
			explode = &b
		}
		out = append(out, oasParam{
			name:     name,
			in:       in,
			required: nodeBool(mapGet(item, "required")) || in == "path",
			schema:   schema,
			style:    nodeString(mapGet(item, "style")),
			explode:  explode,
		})
	}
	return out
}

func mergeParams(base, over []oasParam) []oasParam {
	type key struct{ name, in string }
	order := make([]key, 0, len(base)+len(over))
	got := map[key]oasParam{}
	add := func(p oasParam, replace bool) {
		k := key{p.name, p.in}
		if _, ok := got[k]; !ok {
			order = append(order, k)
		} else if !replace {
			return
		}
		got[k] = p
	}
	for _, p := range base {
		add(p, false)
	}
	for _, p := range over {
		add(p, true)
	}
	out := make([]oasParam, 0, len(order))
	for _, k := range order {
		out = append(out, got[k])
	}
	return out
}

func (s *oasServer) validate(op *oasOp, r *http.Request, params map[string]string) ([]string, int) {
	var errs []string
	for _, p := range op.params {
		raws, present := paramValues(p, r, params)
		loc := p.in + "." + p.name
		if !present {
			if p.required {
				errs = append(errs, loc+": required")
			}
			continue
		}
		if p.schema == nil {
			continue
		}
		val, conv := coerceParam(p, raws, s.root)
		if conv != "" {
			errs = append(errs, loc+": "+conv)
			continue
		}
		errs = append(errs, validateSchema(p.schema, val, loc, s.root, 0)...)
	}
	if len(op.bodies) == 0 {
		return errs, 0
	}
	body, err := readLimited(r)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			return nil, http.StatusRequestEntityTooLarge
		}
		errs = append(errs, "body: "+err.Error())
		return errs, 0
	}
	if len(bytes.TrimSpace(body)) == 0 {
		if op.bodyRequired {
			errs = append(errs, "body: required")
		}
		return errs, 0
	}
	ct := mediaTypeOf(r.Header.Get("Content-Type"))
	schema, specCT, ok := matchBody(op.bodies, ct, body)
	if !ok {
		errs = append(errs, fmt.Sprintf("body: unsupported content type %q", ct))
		return errs, 0
	}
	if schema == nil {
		return errs, 0
	}
	if isJSONType(specCT) || (ct == "" && looksJSON(body)) {
		var v any
		if json.Unmarshal(body, &v) != nil {
			errs = append(errs, "body: invalid JSON")
			return errs, 0
		}
		errs = append(errs, validateSchema(schema, v, "body", s.root, 0)...)
		return errs, 0
	}
	errs = append(errs, validateSchema(schema, string(body), "body", s.root, 0)...)
	return errs, 0
}

func paramValues(p oasParam, r *http.Request, params map[string]string) ([]string, bool) {
	switch p.in {
	case "path":
		v, ok := params[p.name]
		if !ok {
			return nil, false
		}
		return []string{v}, true
	case "query":
		vs, ok := r.URL.Query()[p.name]
		if !ok || len(vs) == 0 {
			return nil, false
		}
		return vs, true
	case "header":
		if !headerPresent(r.Header, p.name) {
			return nil, false
		}
		return []string{r.Header.Get(p.name)}, true
	case "cookie":
		c, err := r.Cookie(p.name)
		if err != nil {
			return nil, false
		}
		return []string{c.Value}, true
	default:
		return nil, false
	}
}

func headerPresent(h http.Header, name string) bool {
	if _, ok := h[http.CanonicalHeaderKey(name)]; ok {
		return true
	}
	for k := range h {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

func coerceParam(p oasParam, raws []string, root map[string]any) (any, string) {
	return coerceSchema(p.schema, raws, p, true, root, 0, map[string]bool{})
}

func coerceSchema(schema any, raws []string, p oasParam, top bool, root map[string]any, depth int, seen map[string]bool) (any, string) {
	if depth > maxSampleDepth {
		return nil, "schema is too deep"
	}
	m := flattenForType(schema, root, depth, seen)
	raw := ""
	if len(raws) > 0 {
		raw = raws[0]
	}
	switch schemaType(m) {
	case "integer":
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, "expected integer"
		}
		return n, ""
	case "number":
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, "expected number"
		}
		return n, ""
	case "boolean":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, "expected boolean"
		}
		return b, ""
	case "array":
		parts := raws
		if top && p.in == "query" {
			parts = queryArrayParts(p, raws)
		} else if len(raws) <= 1 {
			parts = splitParam(raw, ",")
		}
		out := make([]any, len(parts))
		for i, part := range parts {
			v, err := coerceSchema(m["items"], []string{strings.TrimSpace(part)}, p, false, root, depth+1, seen)
			if err != "" {
				return nil, err
			}
			out[i] = v
		}
		return out, ""
	default:
		return raw, ""
	}
}

// queryArrayParts honours style and explode. The OpenAPI default for a query
// array is form + explode, which is ?ids=1&ids=2. Comma (or space or pipe)
// splitting is only the explode-false form.
func queryArrayParts(p oasParam, raws []string) []string {
	if len(raws) == 0 {
		return nil
	}
	if queryExplode(p) {
		return raws
	}
	return splitParam(raws[0], p.style)
}

func queryExplode(p oasParam) bool {
	if p.explode != nil {
		return *p.explode
	}
	style := p.style
	if style == "" {
		style = "form"
	}
	return style == "form"
}

func splitParam(raw, style string) []string {
	sep := ","
	switch style {
	case "spaceDelimited":
		sep = " "
	case "pipeDelimited":
		sep = "|"
	}
	parts := strings.Split(raw, sep)
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func flattenForType(schema any, root map[string]any, depth int, seen map[string]bool) map[string]any {
	if depth > maxSampleDepth {
		return map[string]any{}
	}
	m := asMap(schema)
	if m == nil {
		return map[string]any{}
	}
	if ref, ok := m["$ref"].(string); ok {
		if seen[ref] {
			return m
		}
		if seen == nil {
			seen = map[string]bool{}
		}
		seen[ref] = true
		defer delete(seen, ref)
		return flattenForType(resolvePtr(root, ref), root, depth+1, seen)
	}
	if parts, ok := m["allOf"].([]any); ok && len(parts) > 0 {
		return mergeAllOf(append([]any{without(m, "allOf")}, parts...), root, 0)
	}
	return m
}

func matchBody(bodies []oasBody, ct string, raw []byte) (any, string, bool) {
	if ct != "" {
		for _, b := range bodies {
			if mimeMatch(ct, b.contentType) {
				return b.schema, b.contentType, true
			}
		}
		return nil, "", false
	}
	for _, b := range bodies {
		if isJSONType(b.contentType) && looksJSON(raw) {
			return b.schema, b.contentType, true
		}
	}
	if len(bodies) == 1 {
		return bodies[0].schema, bodies[0].contentType, true
	}
	return nil, "", false
}

func pickResponse(rs []oasResp, code int) (oasResp, int, bool) {
	if code != 0 {
		for _, r := range rs {
			if r.class == 0 && r.status == code {
				return r, code, true
			}
		}
		class := code / 100
		for _, r := range rs {
			if r.class == class {
				return r, code, true
			}
		}
		for _, r := range rs {
			if r.class == 0 && r.status == 0 {
				return r, code, true
			}
		}
		return oasResp{}, 0, false
	}
	best := 0
	var chosen oasResp
	found := false
	for _, r := range rs {
		if r.class == 0 && r.status >= 200 && r.status < 300 && (!found || r.status < best) {
			best = r.status
			chosen = r
			found = true
		}
	}
	if found {
		return chosen, best, true
	}
	for _, r := range rs {
		if r.class == 2 {
			return r, http.StatusOK, true
		}
	}
	for _, r := range rs {
		if r.class == 0 && r.status == 0 {
			return r, http.StatusOK, true
		}
	}
	if len(rs) == 0 {
		return oasResp{status: http.StatusOK, medias: []oasMedia{{
			contentType: "application/json", fallback: []byte("{}"),
		}}}, http.StatusOK, true
	}
	chosen = rs[0]
	chosenStatus := responseStatus(chosen)
	for _, r := range rs[1:] {
		st := responseStatus(r)
		if st != 0 && (chosenStatus == 0 || st < chosenStatus) {
			chosen = r
			chosenStatus = st
		}
	}
	if chosenStatus == 0 {
		chosenStatus = http.StatusOK
	}
	return chosen, chosenStatus, true
}

func responseStatus(r oasResp) int {
	if r.class != 0 {
		return r.class * 100
	}
	return r.status
}

func pickMedia(medias []oasMedia, accept string) oasMedia {
	if len(medias) == 0 {
		return oasMedia{contentType: "application/json", fallback: []byte("{}")}
	}
	if strings.TrimSpace(accept) == "" {
		return medias[0]
	}
	for _, a := range splitAccept(accept) {
		if a == "*/*" {
			return medias[0]
		}
		for _, m := range medias {
			if mimeMatch(a, m.contentType) {
				return m
			}
			if strings.HasSuffix(a, "/*") {
				prefix := strings.TrimSuffix(strings.ToLower(a), "*")
				if strings.HasPrefix(strings.ToLower(m.contentType), prefix) {
					return m
				}
			}
		}
	}
	return medias[0]
}

func splitAccept(s string) []string {
	type part struct {
		media string
		q     float64
		index int
	}
	var parts []part
	for i, raw := range strings.Split(s, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		media, params, _ := strings.Cut(raw, ";")
		media = strings.TrimSpace(media)
		if media == "" {
			continue
		}
		q := 1.0
		for params != "" {
			var one string
			one, params, _ = strings.Cut(params, ";")
			k, v, ok := strings.Cut(strings.TrimSpace(one), "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
				continue
			}
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				continue
			}
			q = f
		}
		if q <= 0 {
			continue
		}
		parts = append(parts, part{media: media, q: q, index: i})
	}
	sort.SliceStable(parts, func(a, b int) bool {
		if parts[a].q == parts[b].q {
			return parts[a].index < parts[b].index
		}
		return parts[a].q > parts[b].q
	})
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = p.media
	}
	return out
}

func parsePrefer(h http.Header) (example string, code int) {
	for _, line := range h.Values("Prefer") {
		for _, part := range strings.Split(line, ",") {
			part = strings.TrimSpace(part)
			k, v, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			k = strings.ToLower(strings.TrimSpace(k))
			v, _, _ = strings.Cut(v, ";")
			v = strings.Trim(strings.TrimSpace(v), `"`)
			switch k {
			case "example":
				example = v
			case "code":
				n, err := strconv.Atoi(v)
				if err == nil {
					code = n
				}
			}
		}
	}
	return example, code
}

func encodeBody(ct string, v any) []byte {
	if isJSONType(ct) || ct == "" {
		if s, ok := v.(string); ok {
			t := strings.TrimSpace(s)
			if (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) && json.Valid([]byte(t)) {
				return []byte(t)
			}
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			return []byte("null")
		}
		return bytes.TrimRight(buf.Bytes(), "\n")
	}
	switch t := v.(type) {
	case string:
		return []byte(t)
	case nil:
		return nil
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return nil
		}
		return b
	}
}

func isJSONType(ct string) bool {
	return strings.Contains(strings.ToLower(ct), "json")
}

func isHTTPMethod(m string) bool {
	switch strings.ToLower(m) {
	case "get", "post", "put", "patch", "delete", "head", "options", "trace":
		return true
	default:
		return false
	}
}

func statusKey(k string) (status, class int, ok bool) {
	if strings.EqualFold(k, "default") {
		return 0, 0, true
	}
	up := strings.ToUpper(k)
	if len(up) == 3 && up[1] == 'X' && up[2] == 'X' && up[0] >= '1' && up[0] <= '5' {
		return 0, int(up[0] - '0'), true
	}
	n, err := strconv.Atoi(k)
	if err != nil || n < 100 || n > 599 {
		return 0, 0, false
	}
	return n, 0, true
}

func serverPrefix(root map[string]any) string {
	servers, _ := root["servers"].([]any)
	if len(servers) == 0 {
		return ""
	}
	s := asMap(servers[0])
	if s == nil {
		return ""
	}
	raw, _ := s["url"].(string)
	return urlPath(substituteServerVars(raw, s["variables"]))
}

// substituteServerVars replaces {name} with that variable's default before
// the server URL's path is used as a route prefix. A variable with no
// default is left in place, and urlPath then drops the whole prefix.
func substituteServerVars(raw string, vars any) string {
	m := asMap(vars)
	if m == nil || !strings.Contains(raw, "{") {
		return raw
	}
	return serverVar.ReplaceAllStringFunc(raw, func(match string) string {
		name := match[1 : len(match)-1]
		vm := asMap(m[name])
		if vm == nil {
			return match
		}
		d, _ := vm["default"].(string)
		if d == "" || strings.ContainsAny(d, "{}") {
			return match
		}
		return d
	})
}

var serverVar = regexp.MustCompile(`\{([^{}]+)\}`)

func urlPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "{") {
		return ""
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		raw = u.Path
	}
	if raw == "" || raw == "/" {
		return ""
	}
	if !strings.HasPrefix(raw, "/") {
		raw = "/" + raw
	}
	return strings.TrimRight(raw, "/")
}

func joinBase(prefix, path string) string {
	if prefix == "" || path == prefix || strings.HasPrefix(path, prefix+"/") {
		return ""
	}
	return prefix + path
}

func mimeMatch(got, want string) bool {
	return strings.EqualFold(mediaTypeOf(got), mediaTypeOf(want))
}

func mediaTypeOf(ct string) string {
	if ct == "" {
		return ""
	}
	m, _, err := mime.ParseMediaType(ct)
	if err != nil {
		m, _, _ = strings.Cut(ct, ";")
		return strings.TrimSpace(m)
	}
	return m
}

func docNode(n *yaml.Node) *yaml.Node {
	if n != nil && n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return concrete(n.Content[0])
	}
	return concrete(n)
}

func concrete(n *yaml.Node) *yaml.Node {
	if n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		return n.Alias
	}
	return n
}

func mapGet(n *yaml.Node, key string) *yaml.Node {
	n = concrete(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return concrete(n.Content[i+1])
		}
	}
	return nil
}

func mapPairs(n *yaml.Node) []yamlPair {
	n = concrete(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	out := make([]yamlPair, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, yamlPair{k: n.Content[i].Value, v: concrete(n.Content[i+1])})
	}
	return out
}

func resolveNode(root, n *yaml.Node) *yaml.Node {
	n = concrete(n)
	if n == nil {
		return nil
	}
	if ref := mapGet(n, "$ref"); ref != nil && ref.Kind == yaml.ScalarNode {
		if t := yamlPointer(root, ref.Value); t != nil {
			return t
		}
	}
	return n
}

func yamlPointer(root *yaml.Node, ref string) *yaml.Node {
	if root == nil || !strings.HasPrefix(ref, "#/") {
		return nil
	}
	cur := root
	for _, raw := range strings.Split(ref[2:], "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		cur = concrete(cur)
		if cur == nil {
			return nil
		}
		switch cur.Kind {
		case yaml.MappingNode:
			cur = mapGet(cur, part)
		case yaml.SequenceNode:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(cur.Content) {
				return nil
			}
			cur = concrete(cur.Content[i])
		default:
			return nil
		}
	}
	return concrete(cur)
}

func nodeString(n *yaml.Node) string {
	n = concrete(n)
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

func nodeBool(n *yaml.Node) bool {
	n = concrete(n)
	if n == nil || n.Kind != yaml.ScalarNode {
		return false
	}
	switch strings.ToLower(n.Value) {
	case "true", "yes":
		return true
	default:
		return false
	}
}
