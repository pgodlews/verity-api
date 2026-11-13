package model

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/pgodlews/verity-api/core/verr"
)

// checkKnownFields reports the first key in a decoded YAML/JSON document that
// the model does not define, as a validation_error whose path names the
// object (for example suites[0].cases[1].steps[0]). A misspelt key such as
// "asertions" would otherwise be dropped silently and the step would run
// without its checks. Free-form values (request, config, vars, …) are not
// checked. tag is "yaml" or "json"; JSON keys match case-insensitively, as
// encoding/json does.
func checkKnownFields(doc any, tag string) error {
	return walkKnown(doc, reflect.TypeOf(Project{}), "", tag)
}

func walkKnown(v any, t reflect.Type, path, tag string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		m, ok := asStringMap(v)
		if !ok {
			return nil // a type mismatch is reported by the real decoder
		}
		fields := map[string]reflect.Type{}
		collectFields(t, tag, fields)
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			name, ft, ok := lookupField(fields, k, tag)
			if !ok {
				where := path
				if where == "" {
					where = "(project)"
				}
				return verr.Newf(verr.CodeValidation, "unknown field %q in %s", k, where).
					WithPath(path).WithDetail("field", k)
			}
			if err := walkKnown(m[k], ft, joinPath(path, name), tag); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		list, ok := v.([]any)
		if !ok {
			return nil
		}
		for i, e := range list {
			if err := walkKnown(e, t.Elem(), fmt.Sprintf("%s[%d]", path, i), tag); err != nil {
				return err
			}
		}
	case reflect.Map:
		m, ok := asStringMap(v)
		if !ok {
			return nil
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := walkKnown(m[k], t.Elem(), joinPath(path, k), tag); err != nil {
				return err
			}
		}
	}
	return nil
}

func asStringMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, e := range m {
			out[fmt.Sprint(k)] = e
		}
		return out, true
	}
	return nil, false
}

func collectFields(t reflect.Type, tag string, out map[string]reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, opts, _ := strings.Cut(f.Tag.Get(tag), ",")
		if name == "-" && opts == "" {
			continue
		}
		if strings.Contains(","+opts+",", ",inline,") || (f.Anonymous && name == "") {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				collectFields(ft, tag, out)
				continue
			}
		}
		if name == "" {
			name = f.Name
			if tag == "yaml" {
				name = strings.ToLower(name)
			}
		}
		out[name] = f.Type
	}
}

// lookupField returns the model's name and type for key.
func lookupField(fields map[string]reflect.Type, key, tag string) (string, reflect.Type, bool) {
	if t, ok := fields[key]; ok {
		return key, t, true
	}
	if tag == "json" {
		for name, t := range fields {
			if strings.EqualFold(name, key) {
				return name, t, true
			}
		}
	}
	return "", nil, false
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
