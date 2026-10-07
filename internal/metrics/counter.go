package metrics

import "fmt"

// Counter is a handle over a monotonically increasing metric in a Registry.
type Counter struct {
	reg  *Registry
	name string
}

func (c *Counter) Name() string         { return c.name }
func (c *Counter) Help() string         { return c.metric().help }
func (c *Counter) Type() MetricType     { return TypeCounter }
func (c *Counter) LabelNames() []string { return append([]string(nil), c.metric().labelNames...) }

func (c *Counter) metric() *metricDef {
	c.reg.mu.RLock()
	defer c.reg.mu.RUnlock()
	return c.reg.metrics[c.name]
}

// RegisterCounter registers a counter and returns its update handle.
func (r *Registry) RegisterCounter(name, help string, labelNames []string) (*Counter, error) {
	if err := r.Register(name, help, TypeCounter, labelNames); err != nil {
		return nil, err
	}
	return &Counter{reg: r, name: name}, nil
}

// MustRegisterCounter panics on registration errors.
func (r *Registry) MustRegisterCounter(name, help string, labelNames []string) *Counter {
	c, err := r.RegisterCounter(name, help, labelNames)
	if err != nil {
		panic(err)
	}
	return c
}

// Inc adds one to the counter series identified by labels.
func (c *Counter) Inc(labels Labels) error { return c.Add(labels, 1) }

// Add adds a non-negative delta to the counter series. Negative deltas are
// rejected with ErrNegativeValue; counters never decrease.
func (c *Counter) Add(labels Labels, delta float64) error {
	if delta < 0 {
		return fmt.Errorf("%w: %s delta %v", ErrNegativeValue, c.name, delta)
	}
	return c.reg.mutateSeries(c.name, labels, func(s *seriesState, nowMs int64) {
		s.value += delta
	})
}

// Get reads the current value of one counter series. A missing or expired
// series reads as 0 with no error.
func (c *Counter) Get(labels Labels) (float64, error) {
	s, err := c.reg.readSeries(c.name, labels)
	if err != nil {
		return 0, err
	}
	if s == nil {
		return 0, nil
	}
	return s.value, nil
}
