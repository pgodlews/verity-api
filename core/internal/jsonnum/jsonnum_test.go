package jsonnum

import (
	"encoding/json"
	"testing"
)

func TestDecodeKeepsLosslessFloatsAndExactBigNumbers(t *testing.T) {
	v, err := Decode([]byte(`{"small":1,"dec":1.1,"big":9007199254740993,"exp":1e400,"list":[2.5,12345678901234567890]}`))
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	if m["small"] != float64(1) || m["dec"] != 1.1 {
		t.Fatalf("lossless numbers = %#v %#v", m["small"], m["dec"])
	}
	if m["big"] != json.Number("9007199254740993") || m["exp"] != json.Number("1e400") {
		t.Fatalf("lossy numbers = %#v %#v", m["big"], m["exp"])
	}
	list := m["list"].([]any)
	if list[0] != 2.5 || list[1] != json.Number("12345678901234567890") {
		t.Fatalf("list = %#v", list)
	}
	if _, err := Decode([]byte(`{} trailing`)); err == nil {
		t.Fatal("trailing data accepted")
	}
	if _, err := Decode([]byte(`{`)); err == nil {
		t.Fatal("truncated JSON accepted")
	}
}

func TestRatAndString(t *testing.T) {
	for _, s := range []string{"1/3", "0x10", "NaN", "inf", "", "1_000"} {
		if _, ok := Rat(s); ok {
			t.Errorf("Rat(%q) converted", s)
		}
	}
	a, _ := Rat(0.1)
	b, _ := Rat("0.1")
	if a.Cmp(b) != 0 {
		t.Fatal("0.1 float and string differ")
	}
	if String(json.Number("9007199254740993")) != "9007199254740993" || String(1e6) != "1000000" || String(nil) != "" {
		t.Fatal("String formatting")
	}
	if String(map[string]any{"a": "<b>"}) != `{"a":"<b>"}` {
		t.Fatal("String map")
	}
}
