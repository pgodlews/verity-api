package secrets

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/pgodlews/verity-api/core/verr"
)

// Mask is the placeholder substituted for redacted secret material.
const Mask = "***REDACTED***"

// Redactor masks secret material in arbitrary text. It is the single redaction
// authority for the whole product: console output, structured logs, JSON/HTML/
// JUnit reports, and the GUI inspector all run their output through a Redactor
// so secrets cannot leak from any path.
//
// A Redactor masks two kinds of material:
//
//  1. Known secret literals registered via Add (e.g. values resolved from
//     ${env:...}, OAuth tokens fetched at run time). Exact substring matches
//     are replaced.
//  2. Structural patterns: Authorization header values, common token/password
//     JSON fields, Set-Cookie values, and PEM private-key blocks.
//
// A Redactor is safe for concurrent use.
type Redactor struct {
	parent   *Redactor
	mu       sync.RWMutex
	literals map[string]struct{}
	// ordered is literals sorted longest first, then lexicographically.
	// Maintained on Add (copy-on-write) so Redact (hot) does not sort.
	ordered []string
	// shortList is the values of short as a slice, replaced (never mutated)
	// on Add, so Redact can use it without allocating.
	shortList []*regexp.Regexp
	// short holds secrets under four characters. Masking them as plain
	// substrings would shred ordinary text, so they are masked only where
	// they stand alone (not inside a longer word or number).
	short map[string]*regexp.Regexp
}

// NewRedactor returns an empty Redactor seeded with the given secret literals.
func NewRedactor(literals ...string) *Redactor {
	r := &Redactor{literals: map[string]struct{}{}, short: map[string]*regexp.Regexp{}}
	for _, l := range literals {
		r.Add(l)
	}
	return r
}

// Add registers a secret literal to be masked wherever it appears. Values
// under four characters are masked only as standalone tokens (see short);
// empty values are ignored.
func (r *Redactor) Add(secret string) {
	if secret == "" {
		return
	}
	if r.parent != nil {
		r.parent.Add(secret)
		return
	}
	r.addLocal(secret)
}

func shortSecretPattern(secret string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(secret) + `($|[^A-Za-z0-9_])`)
}

func orderedLiterals(literals map[string]struct{}) []string {
	ordered := make([]string, 0, len(literals))
	for lit := range literals {
		ordered = append(ordered, lit)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if len(ordered[i]) != len(ordered[j]) {
			return len(ordered[i]) > len(ordered[j])
		}
		return ordered[i] < ordered[j]
	})
	return ordered
}

// insertOrdered returns a new slice with lit inserted at its sorted position
// (longest first, then lexicographic). It copies rather than shifting in
// place, because Redact may still be reading the previous slice.
func insertOrdered(ordered []string, lit string) []string {
	i := sort.Search(len(ordered), func(i int) bool {
		o := ordered[i]
		if len(o) != len(lit) {
			return len(o) < len(lit)
		}
		return o >= lit
	})
	out := make([]string, 0, len(ordered)+1)
	out = append(out, ordered[:i]...)
	out = append(out, lit)
	return append(out, ordered[i:]...)
}

// AddAll registers many secret literals at once.
func (r *Redactor) AddAll(secrets []string) {
	for _, s := range secrets {
		r.Add(s)
	}
}

// LiteralCount returns the number of registered exact secret literals.
func (r *Redactor) LiteralCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.literals) + len(r.short)
}

// structural patterns applied to every Redact call. Each replaces the secret
// portion with Mask while preserving surrounding structure for readability.
// keywords lists lower-case words at least one of which every match contains;
// Redact skips a pattern when none occurs.
var structuralPatterns = []struct {
	re       *regexp.Regexp
	repl     string
	keywords []string
}{
	// Authorization: Bearer xxx / Basic xxx (header line or JSON value).
	{regexp.MustCompile(`(?i)(authorization"?\s*[:=]\s*"?)(bearer|basic|digest)\s+[^\s",}]+`), "${1}${2} " + Mask,
		[]string{"authorization"}},
	// AWS SigV4: keep the credential scope, mask the signature hex.
	{regexp.MustCompile(`(?i)(authorization"?\s*[:=]\s*"?AWS4-HMAC-SHA256\s+Credential=[^,]+,\s*SignedHeaders=[^,]+,\s*Signature=)[0-9a-f]+`), "${1}" + Mask,
		[]string{"authorization"}},
	// Generic "<secretish-key>": "value" JSON fields.
	{regexp.MustCompile(`(?i)("(?:password|passwd|secret|token|access_token|refresh_token|client_secret|api[_-]?key|apikey|authorization)"\s*:\s*")[^"]+(")`), "${1}" + Mask + "${2}",
		[]string{"passw", "secret", "token", "key", "authorization"}},
	// key=value form for the same sensitive keys (query strings, forms).
	{regexp.MustCompile(`(?i)\b(password|passwd|secret|token|access_token|refresh_token|client_secret|api[_-]?key|apikey)=([^&\s",}]+)`), "${1}=" + Mask,
		[]string{"passw", "secret", "token", "key"}},
	// Set-Cookie / Cookie header values.
	{regexp.MustCompile(`(?i)(set-cookie"?\s*[:=]\s*"?)[^\r\n",]+`), "${1}" + Mask, []string{"cookie"}},
	{regexp.MustCompile(`(?i)(cookie"?\s*[:=]\s*"?)[^\r\n",]+`), "${1}" + Mask, []string{"cookie"}},
	// PEM private key blocks (client keys).
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), Mask,
		[]string{"private key"}},
}

// Redact returns s with all known secret literals and structural secret
// patterns masked.
func (r *Redactor) Redact(s string) string {
	if s == "" {
		return s
	}
	if r.parent != nil {
		s = r.parent.Redact(s)
	}
	// First mask exact known literals (most precise).
	r.mu.RLock()
	lits := r.ordered
	shorts := r.shortList
	r.mu.RUnlock()

	s = maskLiterals(s, lits)
	for _, re := range shorts {
		// Adjacent matches share a separator, so repeat until stable.
		for i := 0; i < 8; i++ {
			next := re.ReplaceAllString(s, "${1}"+Mask+"${2}")
			if next == s {
				break
			}
			s = next
		}
	}
	// Then apply structural patterns, skipping those whose keyword is absent
	// (the regexps are case-insensitive, so they cannot use a fast literal
	// prefix scan on their own).
	lower := strings.ToLower(s)
	if strings.ContainsRune(lower, 'ſ') {
		// U+017F folds to "s" under (?i) but ToLower leaves it; it is the
		// only non-ASCII rune besides the Kelvin sign (which ToLower maps)
		// that case-folds to an ASCII letter.
		lower = strings.ReplaceAll(lower, "ſ", "s")
	}
	if strings.Contains(lower, "digest") {
		s = maskDigestResponses(s)
	}
	for _, p := range structuralPatterns {
		if !containsAny(lower, p.keywords) {
			continue
		}
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

// maskLiterals replaces every byte covered by an occurrence of any literal
// with one Mask per merged run. Masking the union of all matches (rather than
// replacing one literal after another, or leftmost-first) never leaves part of
// a secret visible when two secrets overlap: with secrets "zab" and "abcdef",
// "zabcdef" becomes a single Mask.
func maskLiterals(s string, lits []string) string {
	type span struct{ start, end int }
	var spans []span
	for _, lit := range lits {
		end := -1 // end of this literal's current run, merged on the fly
		for i := 0; i <= len(s)-len(lit); {
			j := strings.Index(s[i:], lit)
			if j < 0 {
				break
			}
			start := i + j
			if end >= start {
				spans[len(spans)-1].end = start + len(lit)
			} else {
				spans = append(spans, span{start, start + len(lit)})
			}
			end = start + len(lit)
			i = start + 1 // overlapping occurrences of the same literal too
		}
	}
	if len(spans) == 0 {
		return s
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var b strings.Builder
	b.Grow(len(s))
	pos, cur := 0, spans[0]
	flush := func() {
		b.WriteString(s[pos:cur.start])
		b.WriteString(Mask)
		pos = cur.end
	}
	for _, sp := range spans[1:] {
		if sp.start < cur.end {
			cur.end = max(cur.end, sp.end)
			continue
		}
		flush()
		cur = sp
	}
	flush()
	b.WriteString(s[pos:])
	return b.String()
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// secretKey matches the JSON keys whose string values the structural
// patterns mask. JSONSecretLeaves extends that to non-string values.
var secretKey = regexp.MustCompile(`(?i)^(password|passwd|secret|token|access_token|refresh_token|client_secret|api[_-]?key|apikey|authorization)$`)

// maxJSONScan bounds the body size JSONSecretLeaves parses.
const maxJSONScan = 4 << 20

// JSONSecretLeaves returns the strings stored under a secret-named key whose
// value is an object or array, e.g. {"token": {"value": "..."}}. The
// structural patterns only mask a string directly under such a key; callers
// register these leaves with the redactor so they are masked everywhere,
// without reformatting the body. Non-JSON or very large bodies return nil.
func JSONSecretLeaves(body []byte) []string {
	if len(body) == 0 || len(body) > maxJSONScan {
		return nil
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil
	}
	var out []string
	var walk func(v any, under bool)
	walk = func(v any, under bool) {
		switch t := v.(type) {
		case map[string]any:
			for k, e := range t {
				walk(e, under || secretKey.MatchString(k))
			}
		case []any:
			for _, e := range t {
				walk(e, under)
			}
		case string:
			if under && t != "" {
				out = append(out, t)
			}
		}
	}
	walk(v, false)
	return out
}

// RedactBytes is the []byte convenience form of Redact.
func (r *Redactor) RedactBytes(b []byte) []byte {
	return []byte(r.Redact(string(b)))
}

// RedactMap returns a deep copy of a JSON-like map with all string values
// redacted. Keys are preserved. Used by structured reporters.
func (r *Redactor) RedactMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = r.redactAny(v)
	}
	return out
}

// RedactValue redacts every string inside a JSON-like value (string, map,
// slice); other values are returned unchanged.
func (r *Redactor) RedactValue(v any) any { return r.redactAny(v) }

func (r *Redactor) redactAny(v any) any {
	switch t := v.(type) {
	case string:
		return r.Redact(t)
	case map[string]any:
		return r.RedactMap(t)
	case []string:
		out := make([]string, len(t))
		for i, e := range t {
			out[i] = r.Redact(e)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(t))
		for k, e := range t {
			out[k] = r.Redact(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = r.redactAny(e)
		}
		return out
	default:
		return v
	}
}

// looksLikeSecret reports whether a value resembles a committed plaintext
// secret (long high-entropy-ish token, PEM block, or obvious credential).
// Used by validation to warn on literal-looking secrets in project files.
var (
	jwtLike   = regexp.MustCompile(`^eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)
	longToken = regexp.MustCompile(`^[A-Za-z0-9_\-]{24,}$`)
	pemLike   = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
)

// urlUserinfo matches the user:password (or user) in front of @ in a URL.
// The host and path stay so the rest of a git message is still readable.
var urlUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)([^/\s]+)@`)

// urlQuerySecret matches a credential-shaped query parameter. Remote URLs
// sometimes carry the token there instead of in userinfo.
var urlQuerySecret = regexp.MustCompile(`(?i)([?&](?:access_token|refresh_token|client_secret|id_token|api[_-]?key|apikey|password|passwd|secret|private_token|oauth_token|token)=)[^&#\s"'<>]+`)

// extraHeaderEcho matches a dumped http.extraHeader line. The value is often
// an Authorization header; the whole value is masked so a non-standard
// header name does not slip through.
var extraHeaderEcho = regexp.MustCompile(`(?i)((?:http\.)?extraheader\b[^:=\r\n]*[=:]\s*)\S[^\r\n]*`)

// RedactURLCredentials masks userinfo in URLs (https://user:token@host becomes
// https://***REDACTED***@host). Other text is unchanged.
func RedactURLCredentials(s string) string {
	if s == "" || !strings.Contains(s, "://") {
		return s
	}
	return urlUserinfo.ReplaceAllString(s, "${1}"+Mask+"@")
}

// RedactGitOutput masks credentials git may print: URL userinfo, a token in
// a remote URL query string, an echoed http.extraHeader, and the same
// Authorization / secret-field patterns as every other redacted output.
// Plain progress text is unchanged.
func RedactGitOutput(s string) string {
	if s == "" {
		return s
	}
	s = RedactURLCredentials(s)
	s = urlQuerySecret.ReplaceAllString(s, "${1}"+Mask)
	s = extraHeaderEcho.ReplaceAllString(s, "${1}"+Mask)
	return structuralRedactor.Redact(s)
}

// structuralRedactor applies only the structural patterns. Nothing is ever
// added to it, so one instance is shared instead of building one per call.
var structuralRedactor = NewRedactor()

// LooksLikeSecret reports whether s appears to be a hard-coded secret value
// (rather than a ${...} reference). It is a heuristic used only to emit
// warnings; it never blocks execution.
func LooksLikeSecret(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "${") {
		return false
	}
	if pemLike.MatchString(s) || jwtLike.MatchString(s) {
		return true
	}
	if longToken.MatchString(s) {
		return true
	}
	return false
}

// RedactError returns a safe error, preserving structured fields but never an
// unredacted wrapped cause. Use it at boundaries that return errors to callers.
func (r *Redactor) RedactError(err error) error {
	if err == nil {
		return nil
	}
	if ve, ok := verr.As(err); ok {
		msg := ve.Message
		if cause := ve.Unwrap(); cause != nil {
			msg += ": " + cause.Error()
		}
		return &verr.Error{Code: ve.Code, Message: r.Redact(msg), Path: r.Redact(ve.Path), Details: r.RedactMap(ve.Details)}
	}
	return errors.New(r.Redact(err.Error()))
}
