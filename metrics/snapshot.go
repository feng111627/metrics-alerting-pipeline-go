package metrics

import "time"

// SeriesSnapshot 是单条序列在快照时刻的值。
type SeriesSnapshot struct {
	Name   string
	Labels Labels
	Value  Value
}

// Snapshot 是全部序列在同一时刻的一致视图。
type Snapshot struct {
	TakenAt time.Time
	Series  []SeriesSnapshot
}
