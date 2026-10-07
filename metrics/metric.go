package metrics

import (
	"fmt"
	"math"
	"sync"
)

// MetricType 标识指标类别。
type MetricType int

const (
	Counter MetricType = iota
	Gauge
	Histogram
)

func (t MetricType) String() string {
	switch t {
	case Counter:
		return "counter"
	case Gauge:
		return "gauge"
	case Histogram:
		return "histogram"
	}
	return "unknown"
}

// Value 是某一时刻序列值的不可变拷贝。
type Value struct {
	Type    MetricType
	Counter float64
	Gauge   float64
	Hist    HistogramValue
}

// HistogramValue 是直方图的值拷贝。
type HistogramValue struct {
	Buckets []Bucket
	Sum     float64
	Count   uint64
}

// Bucket 是直方图的一个桶（上界，累计计数）。
type Bucket struct {
	UpperBound float64
	Count      uint64
}

// series 是 Registry 内部使用的序列接口：读写都通过各自的锁保证原子。
type series interface {
	Type() MetricType
	Labels() Labels
	// snapshotValue 返回当前值的一致性拷贝（不得读到半更新状态）。
	snapshotValue() Value
}

// counterSeries 单调递增计数器。
type counterSeries struct {
	mu     sync.Mutex
	labels Labels
	v      float64
}

func (s *counterSeries) Type() MetricType { return Counter }
func (s *counterSeries) Labels() Labels   { return s.labels.Clone() }

func (s *counterSeries) add(delta float64) error {
	if delta < 0 || math.IsNaN(delta) || math.IsInf(delta, 0) {
		return fmt.Errorf("%w: counter delta must be a finite number >= 0, got %v", ErrConstraintViolation, delta)
	}
	s.mu.Lock()
	s.v += delta
	s.mu.Unlock()
	return nil
}

func (s *counterSeries) snapshotValue() Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Value{Type: Counter, Counter: s.v}
}

// gaugeSeries 可增可减的仪表。
type gaugeSeries struct {
	mu     sync.Mutex
	labels Labels
	v      float64
}

func (s *gaugeSeries) Type() MetricType { return Gauge }
func (s *gaugeSeries) Labels() Labels   { return s.labels.Clone() }

func (s *gaugeSeries) set(v float64) error {
	if math.IsNaN(v) {
		return fmt.Errorf("%w: gauge value must not be NaN", ErrConstraintViolation)
	}
	s.mu.Lock()
	s.v = v
	s.mu.Unlock()
	return nil
}

func (s *gaugeSeries) add(delta float64) error {
	if math.IsNaN(delta) || math.IsInf(delta, 0) {
		return fmt.Errorf("%w: gauge delta must be finite, got %v", ErrConstraintViolation, delta)
	}
	s.mu.Lock()
	s.v += delta
	s.mu.Unlock()
	return nil
}

func (s *gaugeSeries) snapshotValue() Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Value{Type: Gauge, Gauge: s.v}
}

// histogramSeries 固定桶边界的直方图。
type histogramSeries struct {
	mu     sync.Mutex
	labels Labels
	bounds []float64 // 递增，不含 +Inf
	counts []uint64  // len(bounds)+1，最后一个为 +Inf 桶
	sum    float64
	count  uint64
}

func newHistogramSeries(labels Labels, bounds []float64) *histogramSeries {
	b := make([]float64, len(bounds))
	copy(b, bounds)
	return &histogramSeries{
		labels: labels,
		bounds: b,
		counts: make([]uint64, len(b)+1),
	}
}

func (s *histogramSeries) Type() MetricType { return Histogram }
func (s *histogramSeries) Labels() Labels   { return s.labels.Clone() }

func (s *histogramSeries) observe(v float64) error {
	if math.IsNaN(v) {
		return fmt.Errorf("%w: histogram observation must not be NaN", ErrConstraintViolation)
	}
	s.mu.Lock()
	i := 0
	for i < len(s.bounds) && v > s.bounds[i] {
		i++
	}
	s.counts[i]++
	s.sum += v
	s.count++
	s.mu.Unlock()
	return nil
}

func (s *histogramSeries) snapshotValue() Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	buckets := make([]Bucket, len(s.bounds)+1)
	var cumulative uint64
	for i, b := range s.bounds {
		cumulative += s.counts[i]
		buckets[i] = Bucket{UpperBound: b, Count: cumulative}
	}
	cumulative += s.counts[len(s.bounds)]
	buckets[len(s.bounds)] = Bucket{UpperBound: math.Inf(1), Count: cumulative}
	return Value{
		Type: Histogram,
		Hist: HistogramValue{Buckets: buckets, Sum: s.sum, Count: s.count},
	}
}

// 写入前的值校验，保证非法写入不会创建序列。
func checkCounterDelta(delta float64) error {
	if delta < 0 || math.IsNaN(delta) || math.IsInf(delta, 0) {
		return fmt.Errorf("%w: counter delta must be a finite number >= 0, got %v", ErrConstraintViolation, delta)
	}
	return nil
}

func checkGaugeValue(v float64) error {
	if math.IsNaN(v) {
		return fmt.Errorf("%w: gauge value must not be NaN", ErrConstraintViolation)
	}
	return nil
}

func checkGaugeDelta(delta float64) error {
	if math.IsNaN(delta) || math.IsInf(delta, 0) {
		return fmt.Errorf("%w: gauge delta must be finite, got %v", ErrConstraintViolation, delta)
	}
	return nil
}

func checkObservation(v float64) error {
	if math.IsNaN(v) {
		return fmt.Errorf("%w: histogram observation must not be NaN", ErrConstraintViolation)
	}
	return nil
}
