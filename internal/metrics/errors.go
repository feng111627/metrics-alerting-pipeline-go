package metrics

import "fmt"

// Sentinel errors for registry operations. Tests assert on these with errors.Is.
var (
	ErrDuplicateMetric = registryErr("metric already registered")
	ErrInvalidName     = registryErr("invalid metric name")
	ErrInvalidLabel    = registryErr("invalid label set")
	ErrDuplicateLabel  = registryErr("duplicate label key")
	ErrLabelMismatch   = registryErr("label keys do not match registered label names")
	ErrSeriesLimit     = registryErr("series cardinality limit reached")
	ErrMetricNotFound  = registryErr("metric not found")
	ErrInvalidBuckets  = registryErr("invalid histogram buckets")
	ErrNegativeValue   = registryErr("negative value for counter")
	ErrUnknownType     = registryErr("unknown metric type")
	ErrTooManyLabels   = registryErr("too many labels")
	ErrRegistryClosed  = registryErr("registry closed")
)

type registryErr string

func (e registryErr) Error() string { return string(e) }

func invalidLabelError(key string) error {
	return fmt.Errorf("%w: %q", ErrInvalidLabel, key)
}

func duplicateLabelError(key string) error {
	return fmt.Errorf("%w: %q", ErrDuplicateLabel, key)
}
