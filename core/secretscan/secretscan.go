// Package secretscan finds plaintext credentials in project files, so they
// can be refused before they are saved or committed. Secrets belong in
// ${env:NAME} or ${secret:...} references; the real values live in the
// process environment or an untracked .env file.
//
// A string value is a literal secret when the text outside its ${...}
// references is non-empty and either
//   - its key names a credential (password, token, apiKey, Authorization…),
//     except keys that name a place or kind (tokenUrl, passwordFile), or
//   - it is shaped like a key or token (JWT, PEM, long random string) and it
//     sits where credentials go: auth config, headers, query parameters,
//     variables.
//
// Findings never carry the value itself.
package secretscan

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/pgodlews/verity-api/core/secrets"
)

// Finding is one literal secret. Path leads from the scanned root to the
// value (map keys and sequence indexes as decimal strings).
type Finding struct {
	File string   `json:"file,omitempty"`
	Line int      `json:"line,omitempty"`
	Path []string `json:"path"`
	Key  string   `json:"key"`
	// Reason is "name" (the key names a credential), "shape" (the value
	// looks like a token or key), or "example" (a value in .env.example).
	Reason string `json:"reason"`
	// EnvVar is a suggested NAME for the ${env:NAME} replacement.
	EnvVar string `json:"envVar"`
	// InText marks a literal inside a larger string (an XML or form body, a
	// URL): Path points at that string, and the fix is an edit inside it
	// rather than replacing the whole value.
	InText bool `json:"inText,omitempty"`
}

// PathString renders Path as a.b[0].c.
func (f Finding) PathString() string {
	var b strings.Builder
	for _, p := range f.Path {
		if _, err := strconv.Atoi(p); err == nil {
			b.WriteString("[" + p + "]")
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(p)
	}
	return b.String()
}

// Message is a one-line, value-free description.
func (f Finding) Message() string {
	if f.Reason == "example" {
		return fmt.Sprintf("%s has a value; .env.example lists names only (keep values in an untracked .env)", f.PathString())
	}
	what := "looks like a credential"
	if f.Reason == "shape" {
		what = "looks like a token or key"
	}
	return fmt.Sprintf("%s %s; store it as ${env:%s} and set %s outside the project", f.PathString(), what, f.EnvVar, f.EnvVar)
}

var (
	refPattern = regexp.MustCompile(`\$\{[^}]*\}`)
	// authScheme is the scheme word in front of an Authorization value.
	authScheme = regexp.MustCompile(`(?i)^(bearer|basic|digest|token|apikey|api-key|negotiate|ntlm|aws4-hmac-sha256)\s+`)
	// secretWords match whole words of a key; secretPairs match two adjacent
	// words ("x-api-key", "privateKey").
	secretWords = map[string]bool{"password": true, "passwd": true, "passphrase": true, "secret": true, "token": true,
		"apikey": true, "credential": true, "credentials": true, "authorization": true, "cookie": true, "sessionid": true}
	secretPairs = map[string]bool{"apikey": true, "privatekey": true, "accesskey": true, "sessionid": true, "authtoken": true}
	// placeWords end a key that names where or what a secret is (tokenUrl,
	// passwordFile, tokenType). They only exempt values that look like that:
	// see describesSecret.
	compactSuffixes = []string{"password", "passwd", "secret", "token", "apikey", "privatekey", "accesskey", "credentials"}
	// Place and kind words end a key that names where or what a secret is
	// (tokenUrl, passwordFile, tokenType). Each kind exempts only values of
	// its own shape; anything else is treated as the secret.
	locationWords = map[string]bool{"url": true, "uri": true, "endpoint": true, "path": true, "file": true, "location": true, "dir": true}
	nameWords     = map[string]bool{"name": true, "field": true, "header": true, "param": true}
	kindWords     = map[string]bool{"type": true, "style": true, "mode": true, "format": true, "method": true, "scheme": true}
	timeWords     = map[string]bool{"expires": true, "expiry": true, "ttl": true, "length": true, "lifetime": true}
	urlOrPath     = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9+.-]*://|/|\./|\.\./|~/|[A-Za-z]:\\)|[/\\]`)
	numberLike    = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(ms|s|m|h|d)?$`)
	// A header or field name has a separator ("X-Auth-Token", "access_token")
	// or is a well-known one.
	fieldName  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*([-_.][A-Za-z0-9]+)+$`)
	knownNames = map[string]bool{"authorization": true, "cookie": true, "token": true, "password": true, "apikey": true, "bearer": true}
	// Values a *Type / *Style / *Mode / *Format / *Method key takes.
	kindValues = map[string]bool{"bearer": true, "basic": true, "digest": true, "jwt": true, "opaque": true, "header": true,
		"query": true, "body": true, "form": true, "json": true, "cookie": true, "post": true, "get": true, "s256": true,
		"plain": true, "mac": true, "client_secret_basic": true, "client_secret_post": true, "private_key_jwt": true,
		"refresh_token": true, "access_token": true, "id_token": true, "urn:ietf:params:oauth:token-type:jwt": true}
	// idWords mark keys whose values are identifiers or digests, not secrets
	// (X-Request-Id, CHECKSUM), even when the value is long and random.
	idWords = map[string]bool{"id": true, "ids": true, "uuid": true, "guid": true, "hash": true, "checksum": true, "digest": true,
		"sha": true, "sha1": true, "sha256": true, "md5": true, "etag": true, "trace": true, "span": true, "correlation": true,
		"request": true, "nonce": true, "version": true, "commit": true, "build": true, "revision": true}
)

// words splits a key into lower-case words: "X-Api-Key" -> x, api, key;
// "clientSecret" -> client, secret; "API_TOKEN" -> api, token.
func words(key string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(key)
	for i, r := range rs {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			// camelCase boundary: lower->Upper, or Upper followed by lower
			// after an acronym ("APIKey" -> api, key).
			if unicode.IsUpper(r) && len(cur) > 0 {
				prev := rs[i-1]
				nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
				if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
					flush()
				}
			}
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// secretWord reports whether w is a credential word: one of secretWords (or
// its plural), or a compact name ending in one ("clientsecret",
// "dbpassword", "accesstoken"). "pwd" is not one: it is usually a directory.
func secretWord(w string) bool {
	if secretWords[w] || (strings.HasSuffix(w, "s") && secretWords[strings.TrimSuffix(w, "s")]) {
		return true
	}
	for _, suffix := range compactSuffixes {
		if strings.HasSuffix(w, suffix) {
			return true
		}
	}
	return false
}

// IsSecretName reports whether a key names a credential. A key that ends in
// a place or kind word (tokenUrl) does too; Check decides from the value.
func IsSecretName(key string) bool {
	ws := words(key)
	for i, w := range ws {
		if secretWord(w) || (i+1 < len(ws) && secretPairs[w+ws[i+1]]) {
			return true
		}
	}
	return false
}

// describesSecret reports whether a credential-named key that ends in a
// place or kind word holds metadata about the secret rather than the secret:
// tokenUrl: https://..., passwordFile: secrets/pw, tokenType: Bearer,
// tokenHeader: X-Auth-Token, tokenTtl: 3600. When it cannot tell, it says no:
// "API_TOKEN_NAME: hunter" is reported.
func describesSecret(key, lit string) bool {
	ws := words(key)
	if len(ws) < 2 {
		return false
	}
	switch last := ws[len(ws)-1]; {
	case locationWords[last]:
		return urlOrPath.MatchString(lit)
	case nameWords[last]:
		return fieldName.MatchString(lit) || knownNames[strings.ToLower(lit)]
	case kindWords[last]:
		return kindValues[strings.ToLower(lit)]
	case timeWords[last]:
		return numberLike.MatchString(lit)
	}
	return false
}

// normalize lower-cases k and drops separators: "X-Api-Key" -> "xapikey".
func normalize(k string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, k)
}

// Literal returns the text of v outside ${...} references, trimmed, with an
// Authorization scheme word ("Bearer ") removed when key is an
// Authorization header: "Bearer ${env:T}" has no literal secret.
func Literal(key, v string) string {
	lit := strings.TrimSpace(v)
	if strings.Contains(normalize(key), "authorization") {
		lit = strings.TrimSpace(authScheme.ReplaceAllString(lit, ""))
	}
	return strings.TrimSpace(refPattern.ReplaceAllString(lit, ""))
}

// Check reports whether value under key is a literal secret, and why.
// credentialPlace says the value sits where credentials go (auth config,
// headers, query, variables), which enables the shape rule.
func Check(key, value string, credentialPlace bool) (bool, string) {
	lit := Literal(key, value)
	if lit == "" || lit == "REDACTED" || lit == secrets.Mask {
		// REDACTED is the placeholder verity mock import writes for a
		// credential found in a saved example. It is not itself a secret.
		return false, ""
	}
	if IsSecretName(key) && !describesSecret(key, lit) {
		return true, "name"
	}
	if credentialPlace && tokenShaped(key, lit) {
		return true, "shape"
	}
	return false, ""
}

// tokenShaped reports a value that looks like a token or key (JWT, PEM, long
// random string). Long IDs and digests look the same, so a key that names
// one (X-Request-Id, CHECKSUM, ETag) is exempt; JWTs and PEM keys never are.
func tokenShaped(key, lit string) bool {
	if !secrets.LooksLikeSecret(lit) {
		return false
	}
	if strings.HasPrefix(lit, "eyJ") || strings.Contains(lit, "PRIVATE KEY") {
		return true
	}
	for _, w := range words(key) {
		if idWords[w] {
			return false
		}
	}
	return true
}

// credentialPlace reports whether a value at path sits where credentials go.
func credentialPlace(path []string) bool {
	for i, p := range path {
		switch p {
		case "headers", "query", "variables", "vars":
			return true
		case "config":
			// auth[n].config.* (auth definitions), not assertion config.
			if i >= 2 && path[i-2] == "auth" {
				return true
			}
		}
	}
	return false
}

// SuggestEnvVar turns a key into an environment variable name:
// "X-Api-Key" -> "X_API_KEY", "clientSecret" -> "CLIENT_SECRET".
func SuggestEnvVar(key string) string {
	var b strings.Builder
	prevLower := false
	for _, r := range key {
		switch {
		case unicode.IsUpper(r):
			if prevLower {
				b.WriteByte('_')
			}
			b.WriteRune(r)
			prevLower = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToUpper(r))
			prevLower = unicode.IsLower(r) || unicode.IsDigit(r)
		default:
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
				b.WriteByte('_')
			}
			prevLower = false
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" {
		return "SECRET"
	}
	if unicode.IsDigit(rune(name[0])) {
		name = "V_" + name
	}
	return name
}

// ScanFile scans a YAML or JSON project file (JSON parses as YAML), every
// document in it, following aliases.
func ScanFile(file string, data []byte) ([]Finding, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var out []Finding
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		walkNode(&doc, nil, map[*yaml.Node]bool{}, func(path []string, key string, n *yaml.Node, line int) {
			for _, f := range checkValue(path, key, n.Value) {
				f.File, f.Line = file, line
				out = append(out, f)
			}
		})
	}
	return out, nil
}

// walkNode visits every scalar mapping value with its path and key. An
// alias is followed at the place it is used (password: *pw is checked as a
// password), with each anchored collection entered once per path to stop
// cycles.
func walkNode(n *yaml.Node, path []string, active map[*yaml.Node]bool, visit func(path []string, key string, n *yaml.Node, line int)) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			walkNode(c, path, active, visit)
		}
	case yaml.AliasNode:
		if n.Alias == nil || active[n.Alias] {
			return
		}
		active[n.Alias] = true
		walkNode(n.Alias, path, active, visit)
		delete(active, n.Alias)
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			p := append(append([]string{}, path...), k.Value)
			line := v.Line
			if v.Kind == yaml.AliasNode && v.Alias != nil && v.Alias.Kind == yaml.ScalarNode {
				v = v.Alias
			}
			if v.Kind == yaml.ScalarNode {
				// Numbers count too: "password: 1234" is still a password.
				if v.Tag == "!!str" || v.Tag == "!!int" || v.Tag == "!!float" {
					visit(p, k.Value, v, line)
				}
				continue
			}
			if k.Value == "<<" { // merge key: the merged map's keys sit here
				p = path
			}
			walkNode(v, p, active, visit)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			walkNode(c, append(append([]string{}, path...), strconv.Itoa(i)), active, visit)
		}
	}
}

// ScanString reports secrets in one scalar the same way ScanFile would if
// the scalar sat at path. A path containing "headers", and mocks[].path, are
// credential places and are also scanned as URLs. High-confidence token
// prefixes inside the text are reported with InText set.
func ScanString(path []string, key, value string) []Finding {
	return checkValue(path, key, value)
}

// checkValue checks one string value: the value itself, and the literals
// inside it when it is a request body, a URL, a header, or a mock path.
func checkValue(path []string, key, value string) []Finding {
	var out []Finding
	place := credentialPlace(path) || headerValue(path) || mockPathKey(path, key)
	whole := false
	if ok, why := Check(key, value, place); ok {
		whole = true
		out = append(out, Finding{Path: path, Key: key, Reason: why, EnvVar: SuggestEnvVar(key)})
	}
	if key == "url" || headerValue(path) || mockPathKey(path, key) {
		out = append(out, urlFindings(path, value)...)
	}
	if key == "body" {
		out = append(out, bodyFindings(path, value)...)
	}
	if !whole {
		out = append(out, inTextFindings(path, key, value)...)
	}
	return out
}

func headerValue(path []string) bool {
	for _, p := range path {
		if p == "headers" {
			return true
		}
	}
	return false
}

func mockPathKey(path []string, key string) bool {
	if key != "path" {
		return false
	}
	for _, p := range path {
		if p == "mocks" {
			return true
		}
	}
	return false
}

// inTextRes are high-confidence token shapes that count even under a key
// that does not name a credential. Lengths follow the issuer's format so
// ordinary words, UUIDs, and hashes do not match.
var inTextRes = []*regexp.Regexp{
	regexp.MustCompile(`(?:\b)(?:AKIA|ASIA)[A-Z0-9]{16}`),
	regexp.MustCompile(`(?:\b)(?:ghp_|gho_|ghu_|ghs_|ghr_)[A-Za-z0-9]{36}`),
	regexp.MustCompile(`(?:\b)github_pat_[A-Za-z0-9_]{22,}`),
	regexp.MustCompile(`(?:\b)(?:sk|rk)_live_[A-Za-z0-9]{16,}`),
	regexp.MustCompile(`(?:\b)xox[abprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`(?:\b)AIza[A-Za-z0-9_-]{35}`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
}

func inTextFindings(path []string, key, value string) []Finding {
	lit := refPattern.ReplaceAllString(value, "")
	if lit == "" || !inTextToken(lit) {
		return nil
	}
	return []Finding{{Path: path, Key: key, Reason: "shape", EnvVar: SuggestEnvVar(key), InText: true}}
}

func inTextToken(value string) bool {
	for _, re := range inTextRes {
		if re.MatchString(value) {
			return true
		}
	}
	return false
}

var formBody = regexp.MustCompile(`^[^\s{<]*=[^\s]*$`)

// bodyFindings looks inside a string body: JSON (nested paths, editable like
// a json body), XML/SOAP (elements, CDATA, attributes) and form-encoded
// pairs (both in text).
func bodyFindings(path []string, body string) []Finding {
	text := strings.TrimSpace(body)
	switch {
	case text == "":
		return nil
	case text[0] == '{' || text[0] == '[':
		if found, ok := jsonTextFindings(path, text); ok {
			return found
		}
		return nil
	case text[0] == '<':
		return xmlFindings(path, text)
	case formBody.MatchString(text):
		return pairFindings(path, text)
	}
	return nil
}

// jsonTextFindings walks JSON text token by token, so every member is seen,
// including duplicate keys a map would collapse. ok is false when the text
// is not exactly one JSON value.
func jsonTextFindings(root []string, text string) (out []Finding, ok bool) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var value func(path []string, key string) error
	value = func(path []string, key string) error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				for dec.More() {
					kt, err := dec.Token()
					if err != nil {
						return err
					}
					k, _ := kt.(string)
					if err := value(append(append([]string{}, path...), k), k); err != nil {
						return err
					}
				}
			case '[':
				for i := 0; dec.More(); i++ {
					if err := value(append(append([]string{}, path...), strconv.Itoa(i)), key); err != nil {
						return err
					}
				}
			}
			_, err := dec.Token() // closing delimiter
			return err
		case string:
			if key != "" {
				out = append(out, checkValue(path, key, t)...)
			}
		case json.Number:
			if key != "" {
				out = append(out, checkValue(path, key, t.String())...)
			}
		}
		return nil
	}
	if err := value(root, ""); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false // trailing content: not one JSON value
	}
	return out, true
}

// xmlFindings checks element text (CDATA included) and attributes by their
// local names: <wsse:Password>, <Password><![CDATA[...]]>, password="...".
func xmlFindings(path []string, text string) []Finding {
	var out []Finding
	report := func(name, value string) {
		if ok, why := Check(name, value, false); ok {
			out = append(out, Finding{Path: path, Key: name, Reason: why, EnvVar: SuggestEnvVar(name), InText: true})
		}
	}
	dec := xml.NewDecoder(strings.NewReader(text))
	dec.Strict = false
	type open struct {
		name string
		text strings.Builder
	}
	var stack []*open
	for {
		tok, err := dec.Token()
		if err != nil {
			break // EOF or malformed: report what was read
		}
		switch t := tok.(type) {
		case xml.StartElement:
			for _, a := range t.Attr {
				report(a.Name.Local, a.Value)
			}
			stack = append(stack, &open{name: t.Name.Local})
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text.Write(t)
			}
		case xml.EndElement:
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				report(top.name, top.text.String())
			}
		}
	}
	return out
}

// urlFindings reports a password in the user-info part and credential query
// parameters ("?api_key=abc") of a URL.
func urlFindings(path []string, raw string) []Finding {
	prot := protectRefs(raw)
	var out []Finding
	if u, err := url.Parse(prot.text); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok && Literal("password", prot.restore(pw)) != "" {
			out = append(out, Finding{Path: path, Key: "password", Reason: "name", EnvVar: "URL_PASSWORD", InText: true})
		}
	}
	if _, query, ok := strings.Cut(raw, "?"); ok {
		query, _, _ = strings.Cut(query, "#")
		out = append(out, pairFindings(path, query)...)
	}
	return out
}

// pairFindings checks key=value pairs separated by & or ; (a query string or
// a form body). Each pair is decoded on its own, so one malformed pair does
// not hide the others; a pair that does not decode is checked as written.
func pairFindings(path []string, text string) []Finding {
	var out []Finding
	seen := map[string]bool{}
	for _, pair := range strings.FieldsFunc(text, func(r rune) bool { return r == '&' || r == ';' }) {
		k, v, _ := strings.Cut(pair, "=")
		if dk, err := url.QueryUnescape(k); err == nil {
			k = dk
		}
		if dv, err := url.QueryUnescape(v); err == nil {
			v = dv
		}
		if seen[k] {
			continue
		}
		if ok, why := Check(k, v, true); ok {
			seen[k] = true
			out = append(out, Finding{Path: path, Key: k, Reason: why, EnvVar: SuggestEnvVar(k), InText: true})
		}
	}
	return out
}

// protected is text with each ${...} reference swapped for a placeholder
// that survives URL parsing. The placeholder prefix is chosen so it does not
// occur in the original text: authored "VERITYREF0X" cannot pose as one.
type protected struct {
	text   string
	prefix string
	refs   []string
}

func protectRefs(s string) protected {
	prefix := "VERITYREF"
	for strings.Contains(s, prefix) {
		prefix += "Q"
	}
	var refs []string
	text := refPattern.ReplaceAllStringFunc(s, func(m string) string {
		refs = append(refs, m)
		return prefix + strconv.Itoa(len(refs)-1) + "X"
	})
	return protected{text: text, prefix: prefix, refs: refs}
}

func (p protected) restore(s string) string {
	for i := len(p.refs) - 1; i >= 0; i-- { // 10 before 1
		s = strings.ReplaceAll(s, p.prefix+strconv.Itoa(i)+"X", p.refs[i])
	}
	return s
}

// Sensitive reports whether value, shown under key, contains a literal secret.
// credentialPlace is true for auth config, headers, query, and variables.
// Maps and slices are walked with their real keys (and struct fields with
// their json names). A collection is entered once, so a YAML anchor that
// points at itself cannot overflow the stack. A string under "url" or "body"
// is also scanned with the phase-1 URL, JSON-token, XML, and form scanners,
// so duplicate JSON keys and numeric scalars are seen in the original text.
// Callers that get true should replace the whole displayed value; this
// function does not rewrite it.
//
// Descendants of headers, query, variables, and vars are credential places,
// the same rule as phase-1 credentialPlace. config is a credential place only
// when it sits under auth (an auth definition, not an assertion config).
func Sensitive(key string, value any, credentialPlace bool) bool {
	return sensitive(key, value, walkCtx{place: credentialPlace}, map[uintptr]bool{})
}

// walkCtx carries the phase-1 credential-place rule down the tree. place is
// sticky for descendants. underAuth is sticky so a config key inside an auth
// definition becomes a credential place without treating assertion config
// the same way.
type walkCtx struct {
	place     bool
	underAuth bool
}

func (c walkCtx) descend(key string) walkCtx {
	switch key {
	case "headers", "query", "variables", "vars":
		c.place = true
	case "auth":
		c.underAuth = true
	case "config":
		if c.underAuth {
			c.place = true
		}
	}
	return c
}

func sensitive(key string, value any, ctx walkCtx, seen map[uintptr]bool) bool {
	if value == nil {
		return false
	}
	ctx = ctx.descend(key)
	switch v := value.(type) {
	case string:
		return stringSensitive(key, v, ctx.place)
	case json.Number:
		ok, _ := Check(key, v.String(), ctx.place)
		return ok
	case map[string]any:
		if !enterCollection(v, seen) {
			return false
		}
		defer leaveCollection(v, seen)
		for k, child := range v {
			if sensitive(k, child, ctx, seen) {
				return true
			}
		}
		return false
	case map[string]string:
		for k, child := range v {
			if stringSensitive(k, child, ctx.descend(k).place) {
				return true
			}
		}
		return false
	case []any:
		if !enterCollection(v, seen) {
			return false
		}
		defer leaveCollection(v, seen)
		for _, child := range v {
			if sensitive(key, child, ctx, seen) {
				return true
			}
		}
		return false
	}
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return false
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.String:
		return stringSensitive(key, rv.String(), ctx.place)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		ok, _ := Check(key, strconv.FormatInt(rv.Int(), 10), ctx.place)
		return ok
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		ok, _ := Check(key, strconv.FormatUint(rv.Uint(), 10), ctx.place)
		return ok
	case reflect.Float32, reflect.Float64:
		ok, _ := Check(key, strconv.FormatFloat(rv.Float(), 'f', -1, 64), ctx.place)
		return ok
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return false
		}
		if rv.IsNil() {
			return false
		}
		if !enterValue(rv, seen) {
			return false
		}
		defer leaveValue(rv, seen)
		iter := rv.MapRange()
		for iter.Next() {
			child := iter.Value()
			var cv any
			if child.IsValid() && child.CanInterface() {
				cv = child.Interface()
			}
			if sensitive(iter.Key().String(), cv, ctx, seen) {
				return true
			}
		}
		return false
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice {
			if rv.IsNil() {
				return false
			}
			if !enterValue(rv, seen) {
				return false
			}
			defer leaveValue(rv, seen)
		}
		for i := 0; i < rv.Len(); i++ {
			item := rv.Index(i)
			var cv any
			if item.CanInterface() {
				cv = item.Interface()
			}
			if sensitive(key, cv, ctx, seen) {
				return true
			}
		}
		return false
	case reflect.Struct:
		rt := rv.Type()
		for i := 0; i < rv.NumField(); i++ {
			f := rt.Field(i)
			if f.PkgPath != "" {
				continue
			}
			name, skip := jsonFieldName(f)
			if skip {
				continue
			}
			item := rv.Field(i)
			var cv any
			if item.CanInterface() {
				cv = item.Interface()
			}
			if sensitive(name, cv, ctx, seen) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// enterCollection records a map or slice so a YAML anchor cycle returns
// instead of recursing. A nil collection is not recorded. false means this
// value is already on the stack.
func enterCollection(v any, seen map[uintptr]bool) bool {
	return enterValue(reflect.ValueOf(v), seen)
}

func leaveCollection(v any, seen map[uintptr]bool) {
	leaveValue(reflect.ValueOf(v), seen)
}

func enterValue(rv reflect.Value, seen map[uintptr]bool) bool {
	if rv.Kind() != reflect.Map && rv.Kind() != reflect.Slice {
		return true
	}
	if rv.IsNil() {
		return true
	}
	ptr := rv.Pointer()
	if ptr == 0 {
		return true
	}
	if seen[ptr] {
		return false
	}
	seen[ptr] = true
	return true
}

func leaveValue(rv reflect.Value, seen map[uintptr]bool) {
	if rv.Kind() != reflect.Map && rv.Kind() != reflect.Slice || rv.IsNil() {
		return
	}
	delete(seen, rv.Pointer())
}

func jsonFieldName(f reflect.StructField) (name string, skip bool) {
	tag := f.Tag.Get("json")
	if tag == "" {
		return f.Name, false
	}
	name, _, _ = strings.Cut(tag, ",")
	if name == "-" || name == "" {
		if name == "-" {
			return "", true
		}
		return f.Name, false
	}
	return name, false
}

func stringSensitive(key, value string, place bool) bool {
	if value == "" {
		return false
	}
	path := []string{key}
	if place {
		path = []string{"headers", key}
	}
	return len(checkValue(path, key, value)) > 0
}

// ScanValue scans a decoded JSON-like value (for example a step from the
// GUI). Paths are relative to v; there are no line numbers.
func ScanValue(v any) []Finding {
	return scanValueAt(v, nil)
}

func scanValueAt(v any, root []string) []Finding {
	var out []Finding
	var walk func(v any, path []string)
	walk = func(v any, path []string) {
		switch t := v.(type) {
		case map[string]any:
			for k, e := range t {
				p := append(append([]string{}, path...), k)
				var s string
				switch e := e.(type) {
				case string:
					s = e
				case float64, int, int64, json.Number:
					s = fmt.Sprint(e)
				default:
					walk(e, p)
					continue
				}
				out = append(out, checkValue(p, k, s)...)
			}
		case []any:
			for i, e := range t {
				walk(e, append(append([]string{}, path...), strconv.Itoa(i)))
			}
		}
	}
	walk(v, root)
	sortFindings(out)
	return out
}

func sortFindings(f []Finding) {
	// Map iteration is random; keep output stable for the GUI and tests.
	sort.SliceStable(f, func(i, j int) bool {
		a, b := f[i].PathString(), f[j].PathString()
		if a != b {
			return a < b
		}
		return f[i].Key < f[j].Key
	})
}
