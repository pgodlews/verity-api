package load

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/pgodlews/verity-api/core/verr"
)

// Threshold is one latency bound. Step is empty for an overall bound.
// LimitMs is the bound in milliseconds. Op is "<" or "<=".
type Threshold struct {
	Expr    string
	Step    string
	Metric  string
	Op      string
	LimitMs float64
}

// metric names accepted in a threshold expression.
const thresholdMetrics = "p50|p90|p95|p99|max|mean"

var thresholdRE = regexp.MustCompile(`(?i)^(?:(.+):)?(` + thresholdMetrics + `)\s*(<|<=)\s*([0-9]*\.?[0-9]+)\s*(ms|s)$`)

// ParseThreshold parses "p95<500ms" or "GET widget:p95<=1s".
func ParseThreshold(expr string) (Threshold, error) {
	s := strings.TrimSpace(expr)
	m := thresholdRE.FindStringSubmatch(s)
	if m == nil {
		return Threshold{}, verr.Newf(verr.CodeConfig, "invalid threshold %q (want [step:]p95<500ms)", expr)
	}
	v, err := strconv.ParseFloat(m[4], 64)
	if err != nil || v < 0 {
		return Threshold{}, verr.Newf(verr.CodeConfig, "invalid threshold %q", expr)
	}
	unit := strings.ToLower(m[5])
	if unit == "s" {
		v *= 1000
	}
	return Threshold{
		Expr:    s,
		Step:    m[1],
		Metric:  strings.ToLower(m[2]),
		Op:      m[3],
		LimitMs: v,
	}, nil
}

// ParseErrorRate parses a maximum failure rate. "1%" is 0.01. A bare number
// is a fraction in [0, 1].
func ParseErrorRate(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, verr.New(verr.CodeConfig, "max error rate is empty")
	}
	if strings.HasSuffix(s, "%") {
		n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, "%")), 64)
		if err != nil || n < 0 || n > 100 {
			return 0, verr.Newf(verr.CodeConfig, "invalid max error rate %q (want 0%% to 100%%)", s)
		}
		return n / 100, nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n < 0 || n > 1 {
		return 0, verr.Newf(verr.CodeConfig, "invalid max error rate %q (want 1%% or a fraction from 0 to 1)", s)
	}
	return n, nil
}
