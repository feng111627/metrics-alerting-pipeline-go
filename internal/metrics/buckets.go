package metrics

import (
	"fmt"
	"math"
)

// normalizeBuckets copies, sorts and validates user bucket boundaries and
// appends +Inf. Empty input yields the default Prometheus-style buckets.
func normalizeBuckets(in []float64) ([]float64, error) {
	if len(in) == 0 {
		in = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
	}
	bounds := append([]float64(nil), in...)
	for i, b := range bounds {
		if math.IsNaN(b) || math.IsInf(b, -1) {
			return nil, fmt.Errorf("%w: boundary %d is not a finite number", ErrInvalidBuckets, i)
		}
	}
	// insertion sort; bucket counts are tiny.
	for i := 1; i < len(bounds); i++ {
		for j := i; j > 0 && bounds[j-1] > bounds[j]; j-- {
			bounds[j-1], bounds[j] = bounds[j], bounds[j-1]
		}
	}
	for i := 1; i < len(bounds); i++ {
		if bounds[i-1] == bounds[i] {
			return nil, fmt.Errorf("%w: duplicate boundary %v", ErrInvalidBuckets, bounds[i])
		}
	}
	// Drop an explicit +Inf, then append exactly one.
	if len(bounds) > 0 && math.IsInf(bounds[len(bounds)-1], 1) {
		bounds = bounds[:len(bounds)-1]
	}
	return append(bounds, math.Inf(1)), nil
}
