package importers

// Independent importer for Postman Collection v2.0/v2.1 and environment
// exports, written from the publicly documented JSON format and hand-authored
// fixtures. Not affiliated with or endorsed by Postman, Inc.

import (
	"encoding/json"
	"strings"
)

// pmCollection is the subset of a Postman collection the importer reads.
// Unknown fields are ignored.
type pmCollection struct {
	Info struct {
		Name   string `json:"name"`
		Schema string `json:"schema"`
	} `json:"info"`
	Item     []pmItem     `json:"item"`
	Variable []pmVariable `json:"variable"`
	Auth     *pmAuth      `json:"auth"`
	Event    []pmEvent    `json:"event"`
}

// pmItem is either a folder (Item != nil) or a request (Request set).
type pmItem struct {
	Name     string          `json:"name"`
	Item     []pmItem        `json:"item"`
	Request  json.RawMessage `json:"request"`
	Event    []pmEvent       `json:"event"`
	Auth     *pmAuth         `json:"auth"`
	Response []pmExample     `json:"response"`
}

// pmExample is one saved example response (Collection v2.1 item.response[]).
type pmExample struct {
	Name            string          `json:"name"`
	OriginalRequest json.RawMessage `json:"originalRequest"`
	Code            int             `json:"code"`
	Header          json.RawMessage `json:"header"`
	Body            json.RawMessage `json:"body"`
}

func (it pmItem) isFolder() bool { return it.Item != nil || len(it.Request) == 0 }

type pmRequest struct {
	Method string          `json:"method"`
	Header json.RawMessage `json:"header"`
	URL    json.RawMessage `json:"url"`
	Body   *pmBody         `json:"body"`
	Auth   *pmAuth         `json:"auth"`
}

type pmURL struct {
	Raw      string          `json:"raw"`
	Protocol string          `json:"protocol"`
	Host     json.RawMessage `json:"host"`
	Port     string          `json:"port"`
	Path     json.RawMessage `json:"path"`
	Query    []pmKV          `json:"query"`
	Variable []pmKV          `json:"variable"`
}

type pmBody struct {
	Mode       string `json:"mode"`
	Raw        string `json:"raw"`
	URLEncoded []pmKV `json:"urlencoded"`
	FormData   []pmKV `json:"formdata"`
	File       *struct {
		Src string `json:"src"`
	} `json:"file"`
	GraphQL *struct {
		Query     string `json:"query"`
		Variables string `json:"variables"`
	} `json:"graphql"`
	Options struct {
		Raw struct {
			Language string `json:"language"`
		} `json:"raw"`
	} `json:"options"`
	Disabled bool `json:"disabled"`
}

// pmKV is the key/value shape shared by headers, query params, url-encoded
// and form-data fields, and path variables.
type pmKV struct {
	Key         string          `json:"key"`
	Value       json.RawMessage `json:"value"`
	Disabled    bool            `json:"disabled"`
	Type        string          `json:"type"`
	Src         json.RawMessage `json:"src"`
	ContentType string          `json:"contentType"`
}

func (kv pmKV) value() string { return rawString(kv.Value) }

type pmVariable = pmKV

// pmAuth holds the auth type plus its per-type parameters, which v2.1 encodes
// as a [{key,value}] array and v2.0 as a plain object.
type pmAuth struct {
	Type   string
	Params map[string]string
}

func (a *pmAuth) UnmarshalJSON(data []byte) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	a.Type = rawString(obj["type"])
	a.Params = map[string]string{}
	params := obj[a.Type]
	if len(params) == 0 {
		return nil
	}
	var list []pmKV
	if json.Unmarshal(params, &list) == nil {
		for _, kv := range list {
			a.Params[kv.Key] = kv.value()
		}
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(params, &m) == nil {
		for k, v := range m {
			a.Params[k] = rawString(v)
		}
	}
	return nil
}

type pmEvent struct {
	Listen   string `json:"listen"`
	Disabled bool   `json:"disabled"`
	Script   struct {
		Exec json.RawMessage `json:"exec"`
	} `json:"script"`
}

// lines returns the script source as lines (exec is a string array or a
// single string).
func (e pmEvent) lines() []string {
	var list []string
	if json.Unmarshal(e.Script.Exec, &list) == nil {
		var out []string
		for _, l := range list {
			out = append(out, strings.Split(l, "\n")...)
		}
		return out
	}
	return strings.Split(rawString(e.Script.Exec), "\n")
}

// pmEnvironment is a Postman environment export.
type pmEnvironment struct {
	Name   string `json:"name"`
	Values []struct {
		Key     string          `json:"key"`
		Value   json.RawMessage `json:"value"`
		Enabled *bool           `json:"enabled"`
		Type    string          `json:"type"`
	} `json:"values"`
}

// rawString renders a JSON scalar as a string: strings are unquoted, numbers
// and booleans keep their literal text, null/absent is "".
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

// rawStrings decodes a field that is either a string or an array of strings
// or {value} objects (Postman uses all three for url.host and url.path).
func rawStrings(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		if s := rawString(raw); s != "" {
			return []string{s}
		}
		return nil
	}
	var out []string
	for _, el := range list {
		var obj struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(el, &obj) == nil && obj.Value != "" {
			out = append(out, obj.Value)
			continue
		}
		out = append(out, rawString(el))
	}
	return out
}
