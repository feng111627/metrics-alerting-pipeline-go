package metrics

import "errors"

// 可区分的失败原因，调用方可用 errors.Is 判定。
var (
	ErrInvalidName           = errors.New("metrics: invalid metric name")
	ErrInvalidLabel          = errors.New("metrics: invalid label")
	ErrDuplicateRegistration = errors.New("metrics: duplicate registration")
	ErrTypeMismatch          = errors.New("metrics: metric type mismatch")
	ErrConstraintViolation   = errors.New("metrics: constraint violation")
	ErrCardinalityLimit      = errors.New("metrics: series cardinality limit reached")
	ErrUnknownMetric         = errors.New("metrics: unknown metric")
)
