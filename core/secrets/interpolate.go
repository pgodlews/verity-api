// Package secrets centralizes Verity's two security-critical responsibilities:
//
//  1. Interpolation: resolving ${VAR}, ${env:VAR}, and external secret refs in
//     project values at run time, so plaintext secrets never live in project
//     files.
//  2. Redaction: a single Redactor used by every output path (console, logs,
//     JSON/HTML/JUnit reports, GUI inspector) to mask tokens, passwords,
//     cookies, Authorization headers, and client keys.
//
// Keeping both here guarantees one source of truth: any new reporter or
// transport automatically gets correct secret handling by using these utilities.
package secrets

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

// Resolver resolves variable and secret references found during interpolation.
//
// Variables come from the merged scope (environment + suite/case/step vars and
// captured transfers). Secret refs (${env:NAME} and ${secret:...}) are resolved
// against the process environment / external providers and are NEVER persisted.
type Resolver struct {
	// Vars holds ordinary, non-secret variables (later writers override).
	Vars map[string]string
	// Now and Rand supply dynamic function values; replaceable for tests.
	Now  func() time.Time
	Rand io.Reader
	// FileDir is the project directory: ${secret:file:PATH} resolves a
	// relative PATH against it and, unless AllowExternalFiles is set, must
	// stay inside it. An empty FileDir refuses ${secret:file:...} unless
	// AllowExternalFiles is set.
	FileDir string
	// AllowExternalFiles lets ${secret:file:...} read outside FileDir
	// (--allow-external-files, trusted projects only).
	AllowExternalFiles bool
	// secretValues records every resolved secret literal so the Redactor can
	// mask it wherever it later appears. It is process-memory only.
	secretValues map[string]struct{}
}

// Child returns a resolver that starts with r's variables and file policy.
// Set on the child does not affect r. Secrets the child resolves are reported
// by the child's SecretValues.
func (r *Resolver) Child() *Resolver {
	c := NewResolver(r.Vars)
	c.Now, c.Rand = r.Now, r.Rand
	c.FileDir, c.AllowExternalFiles = r.FileDir, r.AllowExternalFiles
	return c
}

// WithFiles sets the ${secret:file:...} policy and returns r.
func (r *Resolver) WithFiles(dir string, allowExternal bool) *Resolver {
	r.FileDir = dir
	r.AllowExternalFiles = allowExternal
	return r
}

// NewResolver builds a Resolver over the given variable map. The map is copied.
func NewResolver(vars map[string]string) *Resolver {
	cp := make(map[string]string, len(vars))
	for k, v := range vars {
		cp[k] = v
	}
	return &Resolver{Vars: cp, Now: time.Now, Rand: rand.Reader, secretValues: map[string]struct{}{}}
}

// Set assigns a plain variable (e.g. a property transfer result).
func (r *Resolver) Set(name, value string) {
	if r.Vars == nil {
		r.Vars = map[string]string{}
	}
	r.Vars[name] = value
}

// SecretValues returns the set of resolved secret literals gathered so far.
// The Redactor uses this to mask exact secret occurrences in output.
func (r *Resolver) SecretValues() []string {
	out := make([]string, 0, len(r.secretValues))
	for v := range r.secretValues {
		out = append(out, v)
	}
	return out
}

// refPattern matches ${...} references. The inner expression may be:
//   - a plain variable name:        ${BASE_URL}
//   - an env reference (secret):    ${env:API_TOKEN}
//   - a dynamic function:           ${fn:uuid}
//   - an external secret reference: ${secret:provider:key}
var refPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// maxInterpolationDepth bounds recursive reference expansion (see
// Interpolate) so a cyclic or pathologically-nested chain of variables fails
// fast with a clear error instead of hanging or blowing the stack.
const maxInterpolationDepth = 8

// Interpolate resolves all ${...} references in s, recursively: if a
// resolved value itself contains ${...} references (e.g. a variable whose
// value is another ${VAR} or ${env:...} reference), those are resolved too,
// up to maxInterpolationDepth. Unknown plain variables are left as-is (so
// partially-templated values can round-trip) unless strict is true, in which
// case an error is returned. Resolved env/secret values are registered for
// redaction regardless of how many levels of indirection produced them.
// Reference cycles (e.g. A -> B -> A) are detected and reported as errors.
func (r *Resolver) Interpolate(s string, strict bool) (string, error) {
	return r.interpolate(s, strict, nil, 0)
}

// interpolate is the recursive core of Interpolate. path tracks the chain of
// reference expressions currently being expanded, used for cycle detection;
// depth is the current nesting depth.
func (r *Resolver) interpolate(s string, strict bool, path []string, depth int) (string, error) {
	var firstErr error
	out := refPattern.ReplaceAllStringFunc(s, func(match string) string {
		inner := match[2 : len(match)-1] // strip ${ and }

		for _, p := range path {
			if p == inner {
				if firstErr == nil {
					chain := append(append([]string{}, path...), inner)
					firstErr = fmt.Errorf("interpolation cycle detected: %s", strings.Join(chain, " -> "))
				}
				return match
			}
		}
		if depth >= maxInterpolationDepth {
			if firstErr == nil {
				firstErr = fmt.Errorf("interpolation depth exceeded %d resolving ${%s}", maxInterpolationDepth, inner)
			}
			return match
		}

		val, secret, ok, err := r.resolveRef(inner)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return match
		}
		if !ok {
			if strict && firstErr == nil {
				firstErr = fmt.Errorf("unresolved reference ${%s}", inner)
			}
			return match
		}

		// Register the raw secret even if nested interpolation fails below.
		if secret && val != "" {
			r.secretValues[val] = struct{}{}
		}
		// Keychain items are opaque credentials, even when they contain ${...}.
		// Expanding their contents could expose them in interpolation errors.
		if refPattern.MatchString(val) && !strings.HasPrefix(inner, "secret:keychain:") {
			nested, nerr := r.interpolate(val, strict, append(append([]string{}, path...), inner), depth+1)
			if nerr != nil {
				if firstErr == nil {
					firstErr = nerr
				}
				return match
			}
			val = nested
		}

		if secret && val != "" {
			r.secretValues[val] = struct{}{}
		}
		return val
	})
	return out, firstErr
}

// resolveRef resolves a single reference expression, returning its value,
// whether it is secret, whether it was found, and any error.
func (r *Resolver) resolveRef(inner string) (val string, secret, ok bool, err error) {
	switch {
	case strings.HasPrefix(inner, "env:"):
		name := strings.TrimPrefix(inner, "env:")
		v, present := os.LookupEnv(name)
		if !present {
			return "", true, false, nil
		}
		return v, true, true, nil
	case strings.HasPrefix(inner, "fn:"):
		v, e := r.resolveFunction(strings.TrimPrefix(inner, "fn:"))
		return v, false, true, e
	case strings.HasPrefix(inner, "secret:"):
		// External secret refs: ${secret:provider:key}. Resolution delegates to
		// a registered provider. The default "env" provider reads an env var.
		spec := strings.TrimPrefix(inner, "secret:")
		if path, isFile := strings.CutPrefix(spec, "file:"); isFile {
			v, found, e := readSecretFile(r.FileDir, path, r.AllowExternalFiles)
			return v, true, found, e
		}
		v, found, e := resolveExternalSecret(spec)
		return v, true, found, e
	default:
		// Plain variable.
		if v, present := r.Vars[inner]; present {
			return v, false, true, nil
		}
		// Fall back to the process environment for convenience. The value is
		// treated as secret, like ${env:NAME}: a ${API_TOKEN} written without
		// "env:" must still be redacted. `verity validate` warns about it.
		if v, present := os.LookupEnv(inner); present {
			return v, true, true, nil
		}
		return "", false, false, nil
	}
}

// InterpolateMap interpolates every string value in a map (non-recursively for
// nested maps/slices it recurses via InterpolateAny).
func (r *Resolver) InterpolateMap(m map[string]any, strict bool) (map[string]any, error) {
	out := make(map[string]any, len(m))
	for k, v := range m {
		nv, err := r.InterpolateAny(v, strict)
		if err != nil {
			return nil, err
		}
		out[k] = nv
	}
	return out, nil
}

// InterpolateAny recursively interpolates strings within arbitrary JSON-like
// values (maps, slices, strings). Other types pass through unchanged.
func (r *Resolver) InterpolateAny(v any, strict bool) (any, error) {
	switch t := v.(type) {
	case string:
		return r.Interpolate(t, strict)
	case map[string]any:
		return r.InterpolateMap(t, strict)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			ne, err := r.InterpolateAny(e, strict)
			if err != nil {
				return nil, err
			}
			out[i] = ne
		}
		return out, nil
	default:
		return v, nil
	}
}
