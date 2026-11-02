package load

import "testing"

func TestParseThreshold(t *testing.T) {
	cases := []struct {
		in      string
		step    string
		metric  string
		op      string
		limitMs float64
		ok      bool
	}{
		{"p95<500ms", "", "p95", "<", 500, true},
		{"p95<=500ms", "", "p95", "<=", 500, true},
		{"p99 < 1s", "", "p99", "<", 1000, true},
		{"mean<=1.5s", "", "mean", "<=", 1500, true},
		{"max<0ms", "", "max", "<", 0, true},
		{"GET widget:p95<300ms", "GET widget", "p95", "<", 300, true},
		{"P50<10MS", "", "p50", "<", 10, true},
		{"nope", "", "", "", 0, false},
		{"p95<fast", "", "", "", 0, false},
		{"p95<2h", "", "", "", 0, false},
	}
	for _, tc := range cases {
		got, err := ParseThreshold(tc.in)
		if tc.ok != (err == nil) {
			t.Errorf("%q ok=%v err=%v", tc.in, tc.ok, err)
			continue
		}
		if !tc.ok {
			continue
		}
		if got.Step != tc.step || got.Metric != tc.metric || got.Op != tc.op || got.LimitMs != tc.limitMs {
			t.Errorf("%q -> %+v", tc.in, got)
		}
	}
}

func TestParseErrorRate(t *testing.T) {
	cases := []struct {
		in  string
		out float64
		ok  bool
	}{
		{"1%", 0.01, true},
		{"0%", 0, true},
		{"100%", 1, true},
		{"200%", 0, false},
		{"100.1%", 0, false},
		{"0.01", 0.01, true},
		{"0", 0, true},
		{"1", 1, true},
		{"2", 0, false},
		{"-1%", 0, false},
		{"fast", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, err := ParseErrorRate(tc.in)
		if tc.ok != (err == nil) {
			t.Errorf("%q ok=%v err=%v", tc.in, tc.ok, err)
			continue
		}
		if tc.ok && got != tc.out {
			t.Errorf("%q = %v, want %v", tc.in, got, tc.out)
		}
	}
}

func TestVUWarning(t *testing.T) {
	if VUWarning(200) != "" || VUWarning(1) != "" {
		t.Fatal("no warning at or below 200")
	}
	if VUWarning(201) == "" || VUWarning(1000) == "" {
		t.Fatal("expected a warning above 200")
	}
	if VUWarning(1001) != "" {
		t.Fatal("over the cap is an error, not a warning")
	}
}
