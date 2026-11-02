package load

import "math"

// Histogram is a fixed-size log-bucket histogram of durations in
// microseconds. Bucket i covers [origin*ratio^i, origin*ratio^(i+1)). The
// bucket count is chosen once, so memory stays flat for a long run. Min,
// max, and the sum behind the mean are the raw samples, including ones
// longer than an hour. A quantile is the upper edge of the bucket that
// holds the nearest-rank sample, which is at most one bucket (about 1%)
// above the sample and never above one hour.
type Histogram struct {
	counts []uint64
	origin float64
	ratio  float64
	n      uint64
	sum    float64
	min    float64
	max    float64
	has    bool
}

const (
	histOriginUS = 1.0               // 1µs, so sub-millisecond samples have a bucket
	histRatio    = 1.01              // about 1% relative width
	histMaxUS    = 60 * 60 * 1000000 // 1 hour, in microseconds
)

// NewHistogram returns an empty histogram that accepts durations in
// microseconds.
func NewHistogram() *Histogram {
	n := int(math.Ceil(math.Log(histMaxUS/histOriginUS)/math.Log(histRatio))) + 1
	return &Histogram{
		counts: make([]uint64, n),
		origin: histOriginUS,
		ratio:  histRatio,
	}
}

// Observe records one sample in microseconds. Negative values and NaN are
// recorded as zero. +Inf saturates at the top of the histogram. A finite
// sample above one hour keeps its exact value in min, max, and the mean;
// only the bucket index is clamped, so a percentile stays inside the range.
func (h *Histogram) Observe(v float64) {
	if math.IsNaN(v) || v < 0 || math.IsInf(v, -1) {
		v = 0
	}
	bucket := v
	if math.IsInf(v, 1) || v > histMaxUS {
		bucket = histMaxUS
	}
	stat := v
	if math.IsInf(v, 1) {
		stat = histMaxUS
	}
	if !h.has || stat < h.min {
		h.min = stat
	}
	if !h.has || stat > h.max {
		h.max = stat
	}
	h.has = true
	h.n++
	h.sum += stat
	h.counts[h.index(bucket)]++
}

func (h *Histogram) index(v float64) int {
	last := len(h.counts) - 1
	if v <= h.origin {
		return 0
	}
	i := int(math.Floor(math.Log(v/h.origin) / math.Log(h.ratio)))
	if i < 0 {
		return 0
	}
	if i > last {
		return last
	}
	return i
}

// Quantile returns an approximation of the q quantile (0–1). An empty
// histogram returns 0. q<=0 is the exact minimum and q>=1 is the exact maximum.
func (h *Histogram) Quantile(q float64) float64 {
	if h.n == 0 {
		return 0
	}
	if q <= 0 {
		return h.min
	}
	if q >= 1 {
		return h.max
	}
	target := uint64(math.Ceil(q * float64(h.n)))
	if target < 1 {
		target = 1
	}
	var cum uint64
	for i, c := range h.counts {
		cum += c
		if cum < target {
			continue
		}
		if i == 0 || h.max <= h.origin {
			if h.max <= h.origin {
				return h.max
			}
			return h.origin
		}
		upper := h.origin * math.Pow(h.ratio, float64(i+1))
		if upper > h.max {
			return h.max
		}
		return upper
	}
	return h.max
}

// Mean returns the exact arithmetic mean, or 0 when no samples were recorded.
func (h *Histogram) Mean() float64 {
	if h.n == 0 {
		return 0
	}
	return h.sum / float64(h.n)
}

// Min returns the smallest sample, or 0 when empty.
func (h *Histogram) Min() float64 {
	if !h.has {
		return 0
	}
	return h.min
}

// Max returns the largest sample, or 0 when empty.
func (h *Histogram) Max() float64 {
	if !h.has {
		return 0
	}
	return h.max
}

// Count is the number of samples.
func (h *Histogram) Count() uint64 { return h.n }
