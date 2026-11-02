package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"google.golang.org/grpc/metadata"

	"github.com/pgodlews/verity-api/core/verr"
)

// metadataKey matches the gRPC metadata token set (lowercase).
var metadataKey = regexp.MustCompile(`^[a-z0-9._-]+$`)

func validateTargetMethod(raw map[string]any) (target, method string, err error) {
	target = stringField(raw, "target")
	method = stringField(raw, "method")
	if target == "" || strings.ContainsAny(target, " \r\n") {
		return "", "", verr.New(verr.CodeConfig, "grpc target is required")
	}
	_, _, method, err = normalizeMethod(method)
	if err != nil {
		return "", "", err
	}
	return target, method, nil
}

func normalizeMethod(method string) (service, name, full string, err error) {
	method = strings.TrimSpace(method)
	method = strings.TrimPrefix(method, "/")
	service, name, ok := strings.Cut(method, "/")
	if !ok || service == "" || name == "" || strings.Contains(name, "/") ||
		strings.ContainsAny(service, " \r\n") || strings.ContainsAny(name, " \r\n") {
		return "", "", "", verr.New(verr.CodeConfig, `grpc method must be "package.Service/Method"`)
	}
	return service, name, service + "/" + name, nil
}

func splitMethod(full string) (service, name string, err error) {
	service, name, _, err = normalizeMethod(full)
	return service, name, err
}

func validateTransport(raw map[string]any) error {
	plaintext, err := optionalBool(raw, "plaintext")
	if err != nil {
		return err
	}
	tlsVal, ok := raw["tls"]
	if !ok || tlsVal == nil {
		return nil
	}
	m, ok := tlsVal.(map[string]any)
	if !ok {
		return verr.New(verr.CodeConfig, "grpc tls must be an object")
	}
	if plaintext {
		return verr.New(verr.CodeConfig, "grpc plaintext and tls cannot both be set")
	}
	for k, v := range m {
		switch k {
		case "insecure":
			if _, ok := v.(bool); !ok {
				return verr.New(verr.CodeConfig, "grpc tls.insecure must be a boolean")
			}
		case "caFile", "serverName":
			s, ok := v.(string)
			if !ok || strings.ContainsAny(s, "\r\n") || (k == "caFile" && s == "") {
				return verr.Newf(verr.CodeConfig, "grpc tls.%s must be a string", k)
			}
		default:
			return verr.Newf(verr.CodeConfig, "grpc tls has unknown field %q", k)
		}
	}
	return nil
}

func validateDescriptorPath(raw map[string]any) error {
	v, ok := raw["descriptorSet"]
	if !ok || v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok || s == "" || strings.ContainsAny(s, "\r\n") {
		return verr.New(verr.CodeConfig, "grpc descriptorSet must be a file path")
	}
	return nil
}

func transportConfig(raw map[string]any) (plaintext bool, tlsMap map[string]any, err error) {
	if err = validateTransport(raw); err != nil {
		return false, nil, err
	}
	plaintext, _ = optionalBool(raw, "plaintext")
	if m, ok := raw["tls"].(map[string]any); ok {
		tlsMap = m
	}
	return plaintext, tlsMap, nil
}

func optionalBool(raw map[string]any, key string) (bool, error) {
	v, ok := raw[key]
	if !ok || v == nil {
		return false, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, verr.Newf(verr.CodeConfig, "grpc %s must be a boolean", key)
	}
	return b, nil
}

func stringField(raw map[string]any, key string) string {
	if raw == nil {
		return ""
	}
	s, _ := raw[key].(string)
	return s
}

func messageBytes(v any) ([]byte, error) {
	if v == nil {
		return []byte("{}"), nil
	}
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return []byte("{}"), nil
		}
		if err := requireJSONObject([]byte(s)); err != nil {
			return nil, err
		}
		return []byte(s), nil
	case map[string]any:
		b, err := json.Marshal(t)
		if err != nil {
			return nil, verr.New(verr.CodeConfig, "grpc message is not valid JSON")
		}
		return b, nil
	default:
		return nil, verr.New(verr.CodeConfig, "grpc message must be a JSON object or a JSON string")
	}
}

func requireJSONObject(b []byte) error {
	trim := []byte(strings.TrimSpace(string(b)))
	if len(trim) == 0 || trim[0] != '{' || !json.Valid(trim) {
		return verr.New(verr.CodeConfig, "grpc message must be a JSON object")
	}
	return nil
}

func metadataHeaders(v any) (http.Header, error) {
	h := http.Header{}
	if v == nil {
		return h, nil
	}
	switch m := v.(type) {
	case map[string]any:
		for k, val := range m {
			if err := addMetaValue(h, k, val); err != nil {
				return nil, err
			}
		}
	case map[string]string:
		for k, val := range m {
			if err := addMetaValue(h, k, val); err != nil {
				return nil, err
			}
		}
	default:
		return nil, verr.New(verr.CodeConfig, "grpc metadata must be a map")
	}
	return h, nil
}

func addMetaValue(h http.Header, key string, val any) error {
	switch t := val.(type) {
	case string:
		return appendMeta(h, key, t)
	case []string:
		for _, s := range t {
			if err := appendMeta(h, key, s); err != nil {
				return err
			}
		}
		return nil
	case []any:
		if len(t) == 0 {
			return verr.Newf(verr.CodeConfig, "grpc metadata %q must not be an empty list", key)
		}
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return verr.Newf(verr.CodeConfig, "grpc metadata %q must be a string or list of strings", key)
			}
			if err := appendMeta(h, key, s); err != nil {
				return err
			}
		}
		return nil
	default:
		return verr.Newf(verr.CodeConfig, "grpc metadata %q must be a string or list of strings", key)
	}
}

func appendMeta(h http.Header, key, val string) error {
	if err := checkMetadata(key, val); err != nil {
		return err
	}
	h.Add(key, val)
	return nil
}

func outgoingMetadata(h http.Header) (metadataMD, error) {
	md := metadataMD{}
	for k, vs := range h {
		for _, v := range vs {
			if err := checkMetadata(k, v); err != nil {
				return nil, err
			}
			md = append(md, metaPair{key: strings.ToLower(k), val: v})
		}
	}
	return md, nil
}

type metaPair struct {
	key string
	val string
}

// metadataMD keeps validated pairs without logging them. It is converted to
// grpc metadata only at send time.
type metadataMD []metaPair

func (m metadataMD) asMD() metadata.MD {
	md := metadata.MD{}
	for _, p := range m {
		md.Append(p.key, p.val)
	}
	return md
}

func checkMetadata(key, val string) error {
	lk := strings.ToLower(key)
	if !metadataKey.MatchString(lk) {
		return verr.Newf(verr.CodeConfig, "grpc metadata key %q is not allowed", key)
	}
	if strings.HasSuffix(lk, "-bin") {
		return verr.Newf(verr.CodeConfig, "grpc binary metadata key %q is not supported", key)
	}
	if strings.ContainsAny(val, "\r\n") {
		return verr.Newf(verr.CodeConfig, "grpc metadata value for %q must not contain newlines", key)
	}
	return nil
}
