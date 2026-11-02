package load

import (
	"math"
	"sort"
	"testing"
)

func TestHistogramExactMinMeanMax(t *testing.T) {
	h := NewHistogram()
	if h.Quantile(0.95) != 0 || h.Mean() != 0 || h.Min() != 0 || h.Max() != 0 || h.Count() != 0 {
		t.Fatalf("empty histogram should be zero")
	}
	for i := 0; i < 1000; i++ {
		h.Observe(42)
	}
	if h.Min() != 42 || h.Max() != 42 || h.Mean() != 42 {
		t.Fatalf("min/mean/max = %v/%v/%v, want 42", h.Min(), h.Mean(), h.Max())
	}
	for _, q := range []float64{0.50, 0.90, 0.95, 0.99} {
		got := h.Quantile(q)
		if math.Abs(got-42)/42 > 0.02 {
			t.Fatalf("quantile %v = %v, want within 2%% of 42", q, got)
		}
	}

	zeros := NewHistogram()
	for i := 0; i < 50; i++ {
		zeros.Observe(0)
	}
	if zeros.Quantile(0.99) != 0 || zeros.Max() != 0 {
		t.Fatalf("zero samples: p99=%v max=%v", zeros.Quantile(0.99), zeros.Max())
	}
}

func TestHistogramAccuracy(t *testing.T) {
	h := NewHistogram()
	samples := make([]float64, 0, 10000)
	for i := 1; i <= 10000; i++ {
		v := float64(i)
		samples = append(samples, v)
		h.Observe(v)
	}
	sort.Float64s(samples)
	if h.Min() != 1 || h.Max() != 10000 {
		t.Fatalf("min/max = %v/%v", h.Min(), h.Max())
	}
	if math.Abs(h.Mean()-5000.5) > 1e-6 {
		t.Fatalf("mean = %v, want 5000.5", h.Mean())
	}
	for _, q := range []float64{0.50, 0.90, 0.95, 0.99} {
		exact := nearestRank(samples, q)
		got := h.Quantile(q)
		if got+1e-9 < exact {
			t.Fatalf("quantile %v = %v, below exact %v", q, got, exact)
		}
		// Bucket upper edges sit about 1% above the sample.
		if got > exact*1.02+1 {
			t.Fatalf("quantile %v = %v, exact %v, outside 2%%", q, got, exact)
		}
	}
	if h.Quantile(0) != h.Min() || h.Quantile(1) != h.Max() {
		t.Fatal("quantile endpoints should be exact min and max")
	}
}

func TestHistogramDoesNotClampExactStats(t *testing.T) {
	h := NewHistogram()
	const twoHours = 2 * 60 * 60 * 1e6 // microseconds
	h.Observe(twoHours)
	h.Observe(twoHours)
	if h.Min() != twoHours || h.Max() != twoHours || h.Mean() != twoHours {
		t.Fatalf("min/mean/max = %v/%v/%v, want %v", h.Min(), h.Mean(), h.Max(), twoHours)
	}
	if q := h.Quantile(0.5); q > 60*60*1e6*1.02 {
		t.Fatalf("percentile %v escaped the one-hour bucket range", q)
	}
}

func TestHistogramMemoryStaysBounded(t *testing.T) {
	h := NewHistogram()
	buckets := len(h.counts)
	for i := 0; i < 100000; i++ {
		h.Observe(float64(i%5000) + 0.5)
	}
	if len(h.counts) != buckets {
		t.Fatalf("bucket count changed from %d to %d", buckets, len(h.counts))
	}
	if h.Count() != 100000 {
		t.Fatalf("count %d", h.Count())
	}
}

func nearestRank(sorted []float64, q float64) float64 {
	rank := int(math.Ceil(q * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
