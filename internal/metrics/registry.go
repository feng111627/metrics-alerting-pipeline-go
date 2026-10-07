package metrics

import (
	"fmt"
	"sort"
	"sync"
)

// Default bounds applied by NewRegistry when a config field is zero.
const (
	defaultMaxSeriesPerMetric = 1000
	defaultMaxLabels          = 32
)

// RegistryConfig configures a Registry.
type RegistryConfig struct {
	// MaxSeriesPerMetric bounds label combinations for every metric. Zero
	// falls back to defaultMaxSeriesPerMetric.
	MaxSeriesPerMetric int
	// MaxLabels bounds the number of labels per series.
	MaxLabels int
	// SeriesTTL excludes series not updated within this duration (ms) from
	// snapshots and reclaims them lazily. Zero means series never expire.
	SeriesTTL int64
	// Policy decides behavior when MaxSeriesPerMetric is reached.
	Policy OverflowPolicy
	// NowMs is an injectable clock; defaults to wall time in milliseconds.
	NowMs func() int64
}

// Registry owns metrics. A single RWMutex serializes every value mutation
// against snapshots, so a snapshot can never observe a half-applied update.
type Registry struct {
	mu      sync.RWMutex
	metrics map[string]*metricDef
	cfg     RegistryConfig
}

// metricDef is the internal representation of a registered metric.
type metricDef struct {
	name       string
	help       string
	typ        MetricType
	labelNames []string // sorted
	buckets    []float64
	series     map[string]*seriesState
	createdAt  int64
}

// seriesState holds the concrete value for one labeled series. It is mutated
// only while the registry write lock is held and copied under read lock.
type seriesState struct {
	labels     Labels
	lastUpdate int64

	value float64 // counter / gauge

	bucketBounds []float64 // histogram
	bucketCounts []int64
	sum          float64
	count        int64
}

// NewRegistry builds a registry, filling default limits for zero fields.
func NewRegistry(cfg RegistryConfig) *Registry {
	if cfg.MaxSeriesPerMetric <= 0 {
		cfg.MaxSeriesPerMetric = defaultMaxSeriesPerMetric
	}
	if cfg.MaxLabels <= 0 {
		cfg.MaxLabels = defaultMaxLabels
	}
	if cfg.NowMs == nil {
		cfg.NowMs = nowWallMs
	}
	return &Registry{
		metrics: make(map[string]*metricDef),
		cfg:     cfg,
	}
}

// Register adds a metric definition. It rejects duplicate names, illegal
// names, illegal or duplicated label names and too many labels.
func (r *Registry) Register(name, help string, typ MetricType, labelNames []string) error {
	return r.register(name, help, typ, labelNames, nil)
}

// RegisterHistogram additionally requires sorted, strictly increasing,
// finite bucket boundaries ending with +Inf added implicitly.
func (r *Registry) RegisterHistogram(name, help string, labelNames []string, buckets []float64) error {
	return r.register(name, help, TypeHistogram, labelNames, buckets)
}

func (r *Registry) register(name, help string, typ MetricType, labelNames []string, buckets []float64) error {
	if !IsValidMetricName(name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	if typ < TypeCounter || typ > TypeHistogram {
		return fmt.Errorf("%w: %d", ErrUnknownType, typ)
	}
	names := append([]string(nil), labelNames...)
	if len(names) > r.cfg.MaxLabels {
		return fmt.Errorf("%w: %d > %d", ErrTooManyLabels, len(names), r.cfg.MaxLabels)
	}
	sort.Strings(names)
	for i, n := range names {
		if !IsValidLabelName(n) {
			return invalidLabelError(n)
		}
		if i > 0 && names[i-1] == n {
			return duplicateLabelError(n)
		}
	}
	var bounds []float64
	if typ == TypeHistogram {
		b, err := normalizeBuckets(buckets)
		if err != nil {
			return err
		}
		bounds = b
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.metrics[name]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateMetric, name)
	}
	r.metrics[name] = &metricDef{
		name:       name,
		help:       help,
		typ:        typ,
		labelNames: names,
		buckets:    bounds,
		series:     make(map[string]*seriesState),
		createdAt:  r.now(),
	}
	return nil
}

// Unregister removes a metric and all of its series. It returns false when
// the metric did not exist.
func (r *Registry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.metrics[name]; !ok {
		return false
	}
	delete(r.metrics, name)
	return true
}

// mutateSeries runs fn against the (possibly newly created) series state.
// Series creation enforces label-key equality, cardinality limits and the
// configured overflow policy; expired series are reclaimed first.
func (r *Registry) mutateSeries(metricName string, labels Labels, fn func(s *seriesState, nowMs int64)) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	def, ok := r.metrics[metricName]
	if !ok {
		return fmt.Errorf("%w: %q", ErrMetricNotFound, metricName)
	}
	if !sameLabelNames(labels.keys, def.labelNames) {
		return fmt.Errorf("%w: metric %q expects %v got %v",
			ErrLabelMismatch, metricName, def.labelNames, labels.keys)
	}

	now := r.now()
	r.reclaimExpiredLocked(def, now)

	key := labels.Key()
	s, exists := def.series[key]
	if !exists {
		if len(def.series) >= r.cfg.MaxSeriesPerMetric {
			victim, evicted := policyFor(r.cfg.Policy).choose(entriesOf(def.series))
			if !evicted {
				return fmt.Errorf("%w: metric %q at %d series",
					ErrSeriesLimit, metricName, r.cfg.MaxSeriesPerMetric)
			}
			s = def.series[victim]
			delete(def.series, victim)
			// Reset the reused slot before applying the new update.
			*s = seriesState{}
		} else {
			s = &seriesState{}
		}
		s.labels = labels
		if def.typ == TypeHistogram {
			s.bucketBounds = def.buckets
			s.bucketCounts = make([]int64, len(def.buckets))
		}
		def.series[key] = s
	}
	fn(s, now)
	s.lastUpdate = now
	return nil
}

func (r *Registry) reclaimExpiredLocked(def *metricDef, now int64) {
	if r.cfg.SeriesTTL <= 0 {
		return
	}
	cutoff := now - r.cfg.SeriesTTL
	for k, s := range def.series {
		if s.lastUpdate <= cutoff {
			delete(def.series, k)
		}
	}
}

// readSeries returns a deep-enough copy of one series for value reads.
func (r *Registry) readSeries(metricName string, labels Labels) (*seriesState, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.metrics[metricName]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrMetricNotFound, metricName)
	}
	if !sameLabelNames(labels.keys, def.labelNames) {
		return nil, fmt.Errorf("%w: metric %q", ErrLabelMismatch, metricName)
	}
	s, ok := def.series[labels.Key()]
	if !ok || r.isExpired(s, r.now()) {
		return nil, nil
	}
	cp := *s
	return &cp, nil
}

func (r *Registry) isExpired(s *seriesState, now int64) bool {
	return r.cfg.SeriesTTL > 0 && s.lastUpdate <= now-r.cfg.SeriesTTL
}

// Snapshot returns a consistent view of every live series at one instant.
// The whole copy happens under one read lock taken at a single timestamp,
// so every series in the result reflects the same moment.
func (r *Registry) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	now := r.now()
	names := make([]string, 0, len(r.metrics))
	for n := range r.metrics {
		names = append(names, n)
	}
	sort.Strings(names)

	snap := Snapshot{TakenAtMs: now}
	for _, n := range names {
		def := r.metrics[n]
		ms := MetricSnapshot{
			Name:       def.name,
			Help:       def.help,
			Type:       def.typ,
			LabelNames: append([]string(nil), def.labelNames...),
		}
		keys := make([]string, 0, len(def.series))
		for k := range def.series {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			s := def.series[k]
			if r.isExpired(s, now) {
				continue
			}
			ss := SeriesSnapshot{Labels: s.labels.Pairs()}
			switch def.typ {
			case TypeCounter, TypeGauge:
				ss.Value = s.value
			case TypeHistogram:
				buckets := make([]BucketSnapshot, len(s.bucketBounds))
				for i := range s.bucketBounds {
					buckets[i] = BucketSnapshot{
						UpperBound: s.bucketBounds[i],
						Cumulative: s.bucketCounts[i],
					}
				}
				ss.Histogram = &HistogramSnapshot{
					Buckets:    buckets,
					Sum:        s.sum,
					Count:      s.count,
					LastUpdate: s.lastUpdate,
				}
			}
			ms.Series = append(ms.Series, ss)
		}
		snap.Metrics = append(snap.Metrics, ms)
	}
	return snap
}

func (r *Registry) now() int64 { return r.cfg.NowMs() }

func sameLabelNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func entriesOf(m map[string]*seriesState) []seriesEntry {
	out := make([]seriesEntry, 0, len(m))
	for k, s := range m {
		out = append(out, seriesEntry{key: k, lastSeen: s.lastUpdate})
	}
	return out
}

// Lookup returns the metric definition existence; mainly used by the
// alerting package and tests.
func (r *Registry) Lookup(name string) (typ MetricType, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.metrics[name]
	if !ok {
		return 0, false
	}
	return def.typ, true
}
