package metrics

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// DefaultMaxSeries 是未显式配置时的序列总数上限。
const DefaultMaxSeries = 10000

// metricFamily 是同名指标族，包含其全部标签组合的序列。
type metricFamily struct {
	name    string
	help    string
	typ     MetricType
	buckets []float64 // 仅直方图使用
	series  map[string]series
}

func (f *metricFamily) newSeries(labels Labels) series {
	switch f.typ {
	case Counter:
		return &counterSeries{labels: labels}
	case Gauge:
		return &gaugeSeries{labels: labels}
	default:
		return newHistogramSeries(labels, f.buckets)
	}
}

// Registry 管理指标注册、更新与快照。
//
// 并发模型：所有写入（Inc/Set/Add/Observe/Delete/注册）至少持有读锁，
// 快照持有写锁，因此快照与任何写入互斥——快照中的每条序列都来自
// 同一逻辑时刻，绝不读到半更新的值。
type Registry struct {
	mu          sync.RWMutex
	families    map[string]*metricFamily
	maxSeries   int
	totalSeries int
}

// Option 配置 Registry。
type Option func(*Registry)

// WithMaxSeries 设置全注册表的序列总数上限。
// 达到上限后，创建新标签组合的写入会被拒绝并返回 ErrCardinalityLimit，
// 已有序列的更新不受影响（既定策略：拒绝新序列，而不是淘汰旧序列）。
func WithMaxSeries(n int) Option {
	return func(r *Registry) {
		if n > 0 {
			r.maxSeries = n
		}
	}
}

// NewRegistry 创建注册表。
func NewRegistry(opts ...Option) *Registry {
	r := &Registry{
		families:  make(map[string]*metricFamily),
		maxSeries: DefaultMaxSeries,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// CounterVec / GaugeVec / HistogramVec 是按标签维数组织的指标族句柄。
type CounterVec struct {
	reg *Registry
	fam *metricFamily
}

type GaugeVec struct {
	reg *Registry
	fam *metricFamily
}

type HistogramVec struct {
	reg *Registry
	fam *metricFamily
}

func (r *Registry) register(name, help string, typ MetricType, buckets []float64) (*metricFamily, error) {
	if err := ValidateMetricName(name); err != nil {
		return nil, err
	}
	if help == "" {
		return nil, fmt.Errorf("%w: help text must not be empty", ErrConstraintViolation)
	}
	if typ == Histogram {
		if len(buckets) == 0 {
			return nil, fmt.Errorf("%w: histogram buckets must not be empty", ErrConstraintViolation)
		}
		prev := math.Inf(-1)
		for _, b := range buckets {
			if math.IsNaN(b) || math.IsInf(b, 0) || b <= prev {
				return nil, fmt.Errorf("%w: histogram buckets must be finite and strictly increasing", ErrConstraintViolation)
			}
			prev = b
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.families[name]; exists {
		return nil, fmt.Errorf("%w: %q", ErrDuplicateRegistration, name)
	}
	fam := &metricFamily{
		name:    name,
		help:    help,
		typ:     typ,
		buckets: append([]float64(nil), buckets...),
		series:  make(map[string]series),
	}
	r.families[name] = fam
	return fam, nil
}

// RegisterCounter 注册计数器指标族。
func (r *Registry) RegisterCounter(name string, help string) (*CounterVec, error) {
	fam, err := r.register(name, help, Counter, nil)
	if err != nil {
		return nil, err
	}
	return &CounterVec{reg: r, fam: fam}, nil
}

// RegisterGauge 注册仪表指标族。
func (r *Registry) RegisterGauge(name string, help string) (*GaugeVec, error) {
	fam, err := r.register(name, help, Gauge, nil)
	if err != nil {
		return nil, err
	}
	return &GaugeVec{reg: r, fam: fam}, nil
}

// RegisterHistogram 注册直方图指标族，buckets 必须有限、非空且严格递增。
func (r *Registry) RegisterHistogram(name string, help string, buckets []float64) (*HistogramVec, error) {
	fam, err := r.register(name, help, Histogram, buckets)
	if err != nil {
		return nil, err
	}
	return &HistogramVec{reg: r, fam: fam}, nil
}

// getOrCreate 查找或创建指定标签组合的序列。
// 达到序列上限时拒绝创建新序列并返回 ErrCardinalityLimit。
func (r *Registry) getOrCreate(fam *metricFamily, labels Labels) (series, error) {
	key := labels.Key()
	r.mu.RLock()
	s, ok := fam.series[key]
	r.mu.RUnlock()
	if ok {
		return s, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := fam.series[key]; ok {
		return s, nil
	}
	if r.totalSeries >= r.maxSeries {
		return nil, fmt.Errorf("%w: max %d series, cannot add %q{%s}",
			ErrCardinalityLimit, r.maxSeries, fam.name, key)
	}
	s = fam.newSeries(labels.Clone())
	fam.series[key] = s
	r.totalSeries++
	return s, nil
}

// Inc 将指定标签组合的计数器加 delta（delta 必须为有限值且 >= 0）。
func (c *CounterVec) Inc(labels Labels, delta float64) error {
	if err := ValidateLabels(labels); err != nil {
		return err
	}
	// 先校验值再创建序列，避免失败的写入留下空序列。
	if err := checkCounterDelta(delta); err != nil {
		return err
	}
	s, err := c.reg.getOrCreate(c.fam, labels)
	if err != nil {
		return err
	}
	c.reg.mu.RLock()
	defer c.reg.mu.RUnlock()
	return s.(*counterSeries).add(delta)
}

// Set 设置仪表值。
func (g *GaugeVec) Set(labels Labels, v float64) error {
	if err := ValidateLabels(labels); err != nil {
		return err
	}
	if err := checkGaugeValue(v); err != nil {
		return err
	}
	s, err := g.reg.getOrCreate(g.fam, labels)
	if err != nil {
		return err
	}
	g.reg.mu.RLock()
	defer g.reg.mu.RUnlock()
	return s.(*gaugeSeries).set(v)
}

// Add 在仪表值上加 delta（可为负）。
func (g *GaugeVec) Add(labels Labels, delta float64) error {
	if err := ValidateLabels(labels); err != nil {
		return err
	}
	if err := checkGaugeDelta(delta); err != nil {
		return err
	}
	s, err := g.reg.getOrCreate(g.fam, labels)
	if err != nil {
		return err
	}
	g.reg.mu.RLock()
	defer g.reg.mu.RUnlock()
	return s.(*gaugeSeries).add(delta)
}

// Observe 向直方图写入一个观测值。
func (h *HistogramVec) Observe(labels Labels, v float64) error {
	if err := ValidateLabels(labels); err != nil {
		return err
	}
	if err := checkObservation(v); err != nil {
		return err
	}
	s, err := h.reg.getOrCreate(h.fam, labels)
	if err != nil {
		return err
	}
	h.reg.mu.RLock()
	defer h.reg.mu.RUnlock()
	return s.(*histogramSeries).observe(v)
}

// Delete 删除指定标签组合的序列；返回是否确有序列被删除。
// 删除后的序列不再出现在后续快照中。
func (r *Registry) Delete(name string, labels Labels) bool {
	key := labels.Key()
	r.mu.Lock()
	defer r.mu.Unlock()
	fam, ok := r.families[name]
	if !ok {
		return false
	}
	if _, ok := fam.series[key]; !ok {
		return false
	}
	delete(fam.series, key)
	r.totalSeries--
	return true
}

// Snapshot 返回全部序列在某一一致时刻的拷贝。
// 快照期间所有写入被阻塞，因此结果等价于某一瞬间的完整状态。
func (r *Registry) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap := Snapshot{TakenAt: time.Now()}
	names := make([]string, 0, len(r.families))
	for name := range r.families {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fam := r.families[name]
		keys := make([]string, 0, len(fam.series))
		for k := range fam.series {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			s := fam.series[k]
			snap.Series = append(snap.Series, SeriesSnapshot{
				Name:   fam.name,
				Labels: s.Labels(),
				Value:  s.snapshotValue(),
			})
		}
	}
	return snap
}
