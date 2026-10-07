package metrics

// Metric is the common interface implemented by metric update handles.
type Metric interface {
	Name() string
	Help() string
	Type() MetricType
	LabelNames() []string
}
