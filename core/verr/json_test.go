package verr

import (
	"encoding/json"
	"errors"
	"testing"
)

// The JSON form carries the public fields only: never the wrapped cause.
func TestErrorJSONOmitsWrappedCause(t *testing.T) {
	for _, e := range []*Error{
		Wrap(CodeTransport, errors.New("dial tcp: secret-cause"), "send"),
		New(CodeConfig, "bad").WithPath("suites[0]"),
	} {
		for _, v := range []any{e, *e} {
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			for k := range got {
				switch k {
				case "code", "message", "path", "details":
				default:
					t.Fatalf("unexpected field %q in %s", k, b)
				}
			}
			if got["code"] != string(e.Code) || got["message"] != e.Message {
				t.Fatalf("json %s", b)
			}
			if (e.Path != "") != (got["path"] != nil) {
				t.Fatalf("path in %s", b)
			}
		}
	}
}
