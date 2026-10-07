package metrics

import "sort"

// Histogram is a handle over a bucketed metric in a Registry.
type Histogram struct {
	reg  *Registry
	name string
}

func (h *Histogram) Name() string         { return h.name }
func (h *Histogram) Help() string         { return h.metric().help }
func (h *Histogram) Type() MetricType     { return TypeHistogram }
func (h *Histogram) LabelNames() []string { return append([]string(nil), h.metric().labelNames...) }

// Buckets returns the configured bucket boundaries including +Inf.
func (h *Histogram) Buckets() []float64 { return append([]float64(nil), h.metric().buckets...) }

func (h *Histogram) metric() *metricDef {
	h.reg.mu.RLock()
	defer h.reg.mu.RUnlock()
	return h.reg.metrics[h.name]
}

// RegisterHistogram registers a histogram and returns its observation handle.
// nil/empty buckets selects default boundaries; buckets must be finite and
// strictly increasing after sorting.
func (r *Registry) RegisterHistogramHandle(name, help string, labelNames []string, buckets []float64) (*Histogram, error) {
	if err := r.RegisterHistogram(name, help, labelNames, buckets); err != nil {
		return nil, err
	}
	return &Histogram{reg: r, name: name}, nil
}

// MustRegisterHistogram panics on registration errors.
func (r *Registry) MustRegisterHistogram(name, help string, labelNames []string, buckets []float64) *Histogram {
	h, err := r.RegisterHistogramHandle(name, help, labelNames, buckets)
	if err != nil {
		panic(err)
	}
	return h
}

// Observe records one value into every cumulative bucket whose upper bound
// is >= v (the last +Inf bucket always receives it).
func (h *Histogram) Observe(labels Labels, v float64) error {
	return h.reg.mutateSeries(h.name, labels, func(s *seriesState, nowMs int64) {
		// sort.Search returns the first bucket with bound >= v.
		i := sort.Search(len(s.bucketBounds), func(i int) bool {
			return v <= s.bucketBounds[i]
		})
		for ; i < len(s.bucketCounts); i++ {
			s.bucketCounts[i]++
		}
		s.sum += v
		s.count++
	})
}

// Get returns the histogram state of one series. A missing/expired series
// yields a zeroed HistogramView aligned with the configured buckets.
func (h *Histogram) Get(labels Labels) (HistogramView, error) {
	s, err := h.reg.readSeries(h.name, labels)
	if err != nil {
		return HistogramView{}, err
	}
	view := HistogramView{Buckets: append([]BucketSnapshot(nil), h.metric().bucketSnapshots()...)}
	if s != nil {
		view.Sum = s.sum
		view.Count = s.count
		for i := range s.bucketCounts {
			view.Buckets[i].Cumulative = s.bucketCounts[i]
		}
	}
	return view, nil
}

// HistogramView is a point-in-time copy of one histogram series.
type HistogramView struct {
	Buckets []BucketSnapshot
	Sum     float64
	Count   int64
}

func (d *metricDef) bucketSnapshots() []BucketSnapshot {
	out := make([]BucketSnapshot, len(d.buckets))
	for i, b := range d.buckets {
		out[i] = BucketSnapshot{UpperBound: b}
	}
	return out
}
