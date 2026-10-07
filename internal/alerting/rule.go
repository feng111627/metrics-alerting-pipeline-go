package alerting

// Rule defines a sliding-window alert against one metric series.
type Rule struct {
	Name      string
	Metric    string
	Labels    map[string]string // exact-match selector; empty selects an unlabeled series
	Op        Operator
	Threshold float64
	// WindowMs is the sliding lookback window. Samples older than
	// now-WindowMs are expired and treated as missing data.
	WindowMs int64
	// ForMs is how long the condition must hold continuously (over every
	// fresh sample in the window) before the rule fires. It must be <=
	// WindowMs; a zero value fires as soon as one matching sample exists.
	ForMs int64
}

// Validate checks the rule for usable parameters.
func (r *Rule) Validate() error {
	if r.Name == "" {
		return invalidRule("empty rule name")
	}
	if r.Metric == "" {
		return invalidRule("rule %q has empty metric", r.Name)
	}
	if r.Op < OpGt || r.Op > OpNe {
		return invalidRule("rule %q has invalid operator %d", r.Name, r.Op)
	}
	if r.WindowMs <= 0 {
		return invalidRule("rule %q has non-positive window %d", r.Name, r.WindowMs)
	}
	if r.ForMs < 0 {
		return invalidRule("rule %q has negative for-duration %d", r.Name, r.ForMs)
	}
	if r.ForMs > r.WindowMs {
		return invalidRule("rule %q: for %d exceeds window %d", r.Name, r.ForMs, r.WindowMs)
	}
	for k := range r.Labels {
		if k == "" {
			return invalidRule("rule %q has empty label key", r.Name)
		}
	}
	return nil
}

// matchesSelector reports whether a series label map satisfies every
// selector pair (exact match; extra series labels are allowed).
func (r *Rule) matchesSelector(labels map[string]string) bool {
	for k, v := range r.Labels {
		if labels[k] != v {
			return false
		}
	}
	return true
}
