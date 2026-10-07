package metrics

// Gauge is a handle over a metric that can move up and down.
type Gauge struct {
	reg  *Registry
	name string
}

func (g *Gauge) Name() string         { return g.name }
func (g *Gauge) Help() string         { return g.metric().help }
func (g *Gauge) Type() MetricType     { return TypeGauge }
func (g *Gauge) LabelNames() []string { return append([]string(nil), g.metric().labelNames...) }

func (g *Gauge) metric() *metricDef {
	g.reg.mu.RLock()
	defer g.reg.mu.RUnlock()
	return g.reg.metrics[g.name]
}

// RegisterGauge registers a gauge and returns its update handle.
func (r *Registry) RegisterGauge(name, help string, labelNames []string) (*Gauge, error) {
	if err := r.Register(name, help, TypeGauge, labelNames); err != nil {
		return nil, err
	}
	return &Gauge{reg: r, name: name}, nil
}

// MustRegisterGauge panics on registration errors.
func (r *Registry) MustRegisterGauge(name, help string, labelNames []string) *Gauge {
	g, err := r.RegisterGauge(name, help, labelNames)
	if err != nil {
		panic(err)
	}
	return g
}

// Set overwrites the gauge value.
func (g *Gauge) Set(labels Labels, value float64) error {
	return g.reg.mutateSeries(g.name, labels, func(s *seriesState, nowMs int64) {
		s.value = value
	})
}

// Add moves the gauge by delta; negative deltas decrease it.
func (g *Gauge) Add(labels Labels, delta float64) error {
	return g.reg.mutateSeries(g.name, labels, func(s *seriesState, nowMs int64) {
		s.value += delta
	})
}

// Inc adds one.
func (g *Gauge) Inc(labels Labels) error { return g.Add(labels, 1) }

// Dec subtracts one.
func (g *Gauge) Dec(labels Labels) error { return g.Add(labels, -1) }

// Get reads the current value of one gauge series. Missing/expired reads 0.
func (g *Gauge) Get(labels Labels) (float64, error) {
	s, err := g.reg.readSeries(g.name, labels)
	if err != nil {
		return 0, err
	}
	if s == nil {
		return 0, nil
	}
	return s.value, nil
}
