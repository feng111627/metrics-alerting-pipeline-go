package metrics

// Snapshot is a point-in-time, consistent view of all registered series.
type Snapshot struct {
	TakenAtMs int64
	Metrics   []MetricSnapshot
}

// MetricSnapshot is the consistent view of one metric and all its series.
type MetricSnapshot struct {
	Name       string
	Help       string
	Type       MetricType
	LabelNames []string
	Series     []SeriesSnapshot
}

// SeriesSnapshot is one labeled series within a MetricSnapshot.
type SeriesSnapshot struct {
	Labels []LabelPair
	// Counter/Gauge fields.
	Value float64
	// Histogram fields.
	Histogram *HistogramSnapshot
}

// HistogramSnapshot is a histogram series at one instant.
type HistogramSnapshot struct {
	Buckets    []BucketSnapshot
	Sum        float64
	Count      int64
	LastUpdate int64
}

// BucketSnapshot is one cumulative bucket of a histogram.
type BucketSnapshot struct {
	UpperBound float64
	Cumulative int64
}
