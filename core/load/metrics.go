package load

import (
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// millis is a millisecond duration printed with microsecond precision
// (three decimal places) so a sub-millisecond sample is visible in JSON.
type millis float64

func (v millis) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatFloat(float64(v), 'f', 3, 64)), nil
}

func (v *millis) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return err
	}
	*v = millis(f)
	return nil
}

// maxDistinctErrors is how many different failure messages a report keeps.
// Later new messages are counted in the totals and dropped from the list.
const maxDistinctErrors = 10

// Latency is one distribution, in milliseconds. Min, mean, and max are exact.
// The percentiles come from a log-bucket histogram of microsecond samples.
// JSON prints each field with three decimal places.
type Latency struct {
	MinMs  millis `json:"minMs"`
	MeanMs millis `json:"meanMs"`
	P50Ms  millis `json:"p50Ms"`
	P90Ms  millis `json:"p90Ms"`
	P95Ms  millis `json:"p95Ms"`
	P99Ms  millis `json:"p99Ms"`
	MaxMs  millis `json:"maxMs"`
}

// StatusCount is how many responses carried one HTTP (or protocol) status.
type StatusCount struct {
	Code  int   `json:"code"`
	Count int64 `json:"count"`
}

// Metrics is the load summary for the whole run or for one step name.
// Identical step names across cases are combined.
type Metrics struct {
	Name              string        `json:"name,omitempty"`
	Requests          int64         `json:"requests"`
	AssertionFailures int64         `json:"assertionFailures"`
	TransportErrors   int64         `json:"transportErrors"`
	OtherErrors       int64         `json:"otherErrors,omitempty"`
	ErrorRate         float64       `json:"errorRate"`
	RPS               float64       `json:"rps"`
	Latency           Latency       `json:"latency"`
	StatusCodes       []StatusCount `json:"statusCodes"`
	Errors            []string      `json:"errors"`
}

func (m Metrics) failures() int64 {
	return m.AssertionFailures + m.TransportErrors + m.OtherErrors
}

type recorder struct {
	mu      sync.Mutex
	red     *secrets.Redactor
	overall *stepRec
	steps   map[string]*stepRec
	order   []string
}

type stepRec struct {
	hist      *Histogram
	requests  int64
	assertF   int64
	transport int64
	other     int64
	status    map[int]int64
	errors    []string
	seen      map[string]struct{}
}

func newRecorder(red *secrets.Redactor) *recorder {
	if red == nil {
		red = secrets.NewRedactor()
	}
	return &recorder{
		red:     red,
		overall: newStepRec(),
		steps:   map[string]*stepRec{},
	}
}

func newStepRec() *stepRec {
	return &stepRec{hist: NewHistogram(), status: map[int]int64{}, seen: map[string]struct{}{}}
}

// record adds one finished step. Skipped and aborted steps are not requests.
func (r *recorder) record(st spi.StepResult) {
	switch st.Status {
	case spi.StatusPassed, spi.StatusFailed, spi.StatusError:
	default:
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	name := st.Name
	sr := r.steps[name]
	if sr == nil {
		sr = newStepRec()
		r.steps[name] = sr
		r.order = append(r.order, name)
	}
	note(r.overall, st, r.red)
	note(sr, st, r.red)
}

func note(s *stepRec, st spi.StepResult, red *secrets.Redactor) {
	s.requests++
	s.hist.Observe(sampleUS(st))
	if st.Response != nil {
		s.status[st.Response.StatusCode]++
	}
	switch st.Status {
	case spi.StatusFailed:
		s.assertF++
		for _, a := range st.Assertions {
			if !a.Passed && a.Message != "" {
				s.addErr(red.Redact(st.Name + ": " + a.Message))
			}
		}
	case spi.StatusError:
		if st.Error != nil && st.Error.Code == string(verr.CodeTransport) {
			s.transport++
		} else {
			s.other++
		}
		if st.Error != nil && st.Error.Message != "" {
			s.addErr(red.Redact(st.Name + ": " + st.Error.Message))
		}
	}
}

func (s *stepRec) addErr(msg string) {
	if msg == "" {
		return
	}
	if _, ok := s.seen[msg]; ok {
		return
	}
	if len(s.errors) >= maxDistinctErrors {
		return
	}
	s.seen[msg] = struct{}{}
	s.errors = append(s.errors, msg)
}

func (r *recorder) snapshot(elapsed time.Duration) (Metrics, []Metrics) {
	r.mu.Lock()
	defer r.mu.Unlock()
	overall := r.overall.metrics("", elapsed)
	steps := make([]Metrics, 0, len(r.order))
	for _, name := range r.order {
		steps = append(steps, r.steps[name].metrics(name, elapsed))
	}
	return overall, steps
}

func (s *stepRec) metrics(name string, elapsed time.Duration) Metrics {
	m := Metrics{
		Name:              name,
		Requests:          s.requests,
		AssertionFailures: s.assertF,
		TransportErrors:   s.transport,
		OtherErrors:       s.other,
		Latency: Latency{
			MinMs:  usToMs(s.hist.Min()),
			MeanMs: usToMs(s.hist.Mean()),
			P50Ms:  usToMs(s.hist.Quantile(0.50)),
			P90Ms:  usToMs(s.hist.Quantile(0.90)),
			P95Ms:  usToMs(s.hist.Quantile(0.95)),
			P99Ms:  usToMs(s.hist.Quantile(0.99)),
			MaxMs:  usToMs(s.hist.Max()),
		},
		Errors: append([]string{}, s.errors...),
	}
	if s.requests > 0 {
		m.ErrorRate = float64(m.failures()) / float64(s.requests)
	}
	if sec := elapsed.Seconds(); sec > 0 {
		m.RPS = float64(s.requests) / sec
	}
	codes := make([]int, 0, len(s.status))
	for c := range s.status {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	m.StatusCodes = make([]StatusCount, 0, len(codes))
	for _, c := range codes {
		m.StatusCodes = append(m.StatusCodes, StatusCount{Code: c, Count: s.status[c]})
	}
	return m
}

func latencyValue(l Latency, metric string) float64 {
	switch metric {
	case "p50":
		return float64(l.P50Ms)
	case "p90":
		return float64(l.P90Ms)
	case "p95":
		return float64(l.P95Ms)
	case "p99":
		return float64(l.P99Ms)
	case "max":
		return float64(l.MaxMs)
	case "mean":
		return float64(l.MeanMs)
	default:
		return 0
	}
}

// sampleUS is the step duration in microseconds. Duration is the precise
// value; DurationMS is the fallback for a result that only set the truncated
// field (a hand-built sample in a test).
func sampleUS(st spi.StepResult) float64 {
	if st.Duration > 0 {
		return float64(st.Duration.Microseconds())
	}
	if st.DurationMS > 0 {
		return float64(st.DurationMS) * 1000
	}
	return 0
}

func usToMs(us float64) millis {
	return millis(us / 1000)
}
