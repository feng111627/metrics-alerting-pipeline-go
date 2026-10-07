package metrics

// OverflowPolicy decides what happens when a metric's series limit is reached.
type OverflowPolicy uint8

const (
	// PolicyReject rejects the new series; the update fails with ErrSeriesLimit.
	PolicyReject OverflowPolicy = iota
	// PolicyEvictOldest evicts the series whose last update is oldest and
	// applies the new update in its place, keeping cardinality bounded.
	PolicyEvictOldest
)

// seriesEntry is the minimal information an overflow policy needs.
type seriesEntry struct {
	key      string
	lastSeen int64
}

// evictionPolicy is the internal interface behind OverflowPolicy values.
type evictionPolicy interface {
	// choose returns the key to evict among the given live series.
	choose(entries []seriesEntry) (string, bool)
}

type rejectPolicy struct{}

func (rejectPolicy) choose(entries []seriesEntry) (string, bool) {
	return "", false
}

type evictOldestPolicy struct{}

func (evictOldestPolicy) choose(entries []seriesEntry) (string, bool) {
	if len(entries) == 0 {
		return "", false
	}
	oldest := 0
	for i := 1; i < len(entries); i++ {
		if entries[i].lastSeen < entries[oldest].lastSeen {
			oldest = i
		}
	}
	return entries[oldest].key, true
}

func policyFor(p OverflowPolicy) evictionPolicy {
	switch p {
	case PolicyEvictOldest:
		return evictOldestPolicy{}
	default:
		return rejectPolicy{}
	}
}
