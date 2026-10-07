package metrics

import (
	"math"
	"testing"
)

func nanVal() float64 { return math.NaN() }
func infVal() float64 { return math.Inf(1) }

var testClockNow int64

func newTestRegistry(t *testing.T, cfg RegistryConfig) *Registry {
	t.Helper()
	testClockNow = 0
	cfg.NowMs = func() int64 { return testClockNow }
	return NewRegistry(cfg)
}

func mustCounter(t *testing.T, r *Registry, name string, labels []string) *Counter {
	t.Helper()
	c, err := r.RegisterCounter(name, "", labels)
	if err != nil {
		t.Fatalf("register counter %s: %v", name, err)
	}
	return c
}

func mustGauge(t *testing.T, r *Registry, name string, labels []string) *Gauge {
	t.Helper()
	g, err := r.RegisterGauge(name, "", labels)
	if err != nil {
		t.Fatalf("register gauge %s: %v", name, err)
	}
	return g
}
