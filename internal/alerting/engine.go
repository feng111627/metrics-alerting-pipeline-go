package alerting

import (
	"sort"
	"sync"
	"time"

	"github.com/feng111627/metrics-alerting-pipeline-go/internal/metrics"
)

// RuleStatus is a rule's state plus transition bookkeeping at one moment.
type RuleStatus struct {
	Rule           *Rule
	State          AlertState
	ChangedAt      int64 // when the state last changed
	FiringSince    int64 // timestamp where the condition became continuously true
	LastValue      float64
	LastTs         int64
	FiredCount     int // how many times the rule transitioned into firing
	RecoveredCount int // how many times the rule transitioned into recovered
}

// Event is one state machine transition observed during evaluation.
type Event struct {
	RuleName string
	From     AlertState
	To       AlertState
	AtMs     int64
	Value    float64
}

// Engine evaluates rules over an in-memory sliding window of samples.
type Engine struct {
	mu    sync.Mutex
	rules map[string]*ruleRuntime
	nowFn func() int64
}

// EngineConfig configures an Engine.
type EngineConfig struct {
	// NowMs is an injectable clock; defaults to wall time milliseconds.
	NowMs func() int64
}

type point struct {
	ts int64
	v  float64
}

// ruleRuntime holds the window samples for a rule's single selected series.
type ruleRuntime struct {
	rule    Rule
	status  RuleStatus
	samples map[int64]float64
}

// NewEngine creates an engine.
func NewEngine(cfg EngineConfig) *Engine {
	nowFn := cfg.NowMs
	if nowFn == nil {
		nowFn = func() int64 { return time.Now().UnixNano() / int64(time.Millisecond) }
	}
	return &Engine{
		rules: make(map[string]*ruleRuntime),
		nowFn: nowFn,
	}
}

// AddRule registers a validated rule. Duplicate names are rejected.
func (e *Engine) AddRule(r Rule) error {
	if err := r.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.rules[r.Name]; exists {
		return duplicateRuleError(r.Name)
	}
	rt := &ruleRuntime{
		rule:    cloneRule(r),
		samples: make(map[int64]float64),
	}
	rt.status = RuleStatus{Rule: &rt.rule, State: StateNoData, ChangedAt: e.nowFn()}
	e.rules[r.Name] = rt
	return nil
}

// RemoveRule removes a rule by name.
func (e *Engine) RemoveRule(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.rules[name]; !ok {
		return false
	}
	delete(e.rules, name)
	return true
}

// Ingest receives one sample routed to every rule selecting the metric and
// labels. Writing the same timestamp again overwrites the stored value at
// most once and never creates a second point, so duplicate deliveries cannot
// double-count; timestamps keep points ordered regardless of arrival order.
func (e *Engine) Ingest(metric string, labels map[string]string, tsMs int64, value float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, rt := range e.rules {
		if rt.rule.Metric != metric || !rt.rule.matchesSelector(labels) {
			continue
		}
		rt.samples[tsMs] = value
	}
}

// IngestSnapshot feeds every counter/gauge series of a metrics.Snapshot into
// matching rules, using the snapshot's single timestamp for every point.
func (e *Engine) IngestSnapshot(snap metrics.Snapshot) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ms := range snap.Metrics {
		if ms.Type == metrics.TypeHistogram {
			continue
		}
		for _, s := range ms.Series {
			labels := make(map[string]string, len(s.Labels))
			for _, p := range s.Labels {
				labels[p.Name] = p.Value
			}
			for _, rt := range e.rules {
				if rt.rule.Metric == ms.Name && rt.rule.matchesSelector(labels) {
					rt.samples[snap.TakenAtMs] = s.Value
				}
			}
		}
	}
}

// Evaluate advances one rule through its state machine at time nowMs and
// returns the state AFTER the transition plus any transition event.
func (e *Engine) Evaluate(name string, nowMs int64) (RuleStatus, Event, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rt, ok := e.rules[name]
	if !ok {
		return RuleStatus{}, Event{}, ruleNotFoundError(name)
	}
	ev := e.evaluateLocked(rt, nowMs)
	return rt.status, ev, nil
}

// EvaluateAll evaluates every rule at the same logical instant and returns
// statuses together with the ordered transition events they produced.
func (e *Engine) EvaluateAll(nowMs int64) ([]RuleStatus, []Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	names := make([]string, 0, len(e.rules))
	for n := range e.rules {
		names = append(names, n)
	}
	sort.Strings(names)
	statuses := make([]RuleStatus, 0, len(names))
	var events []Event
	for _, n := range names {
		if ev := e.evaluateLocked(e.rules[n], nowMs); ev.To != ev.From {
			events = append(events, ev)
		}
		statuses = append(statuses, e.rules[n].status)
	}
	return statuses, events
}

// evaluateLocked contains the state machine. Caller holds e.mu.
//
// Window semantics: samples with ts > now-WindowMs are fresh; anything older
// is no data (stale values are never reused). Among fresh samples ALL must
// satisfy the predicate; the condition "holds since" the oldest continuously
// matching sample, and it fires once that span reaches ForMs.
func (e *Engine) evaluateLocked(rt *ruleRuntime, nowMs int64) Event {
	st := &rt.status
	prev := st.State

	// StateRecovered is a one-shot notification; settle back before deciding.
	if prev == StateRecovered {
		st.State = StateNoData
		st.FiringSince = 0
		prev = StateNoData
	}

	// 1) Drop expired points (inclusive boundary: ts == cutoff is stale).
	cutoff := nowMs - rt.rule.WindowMs
	for ts := range rt.samples {
		if ts <= cutoff {
			delete(rt.samples, ts)
		}
	}

	// 2) Summarize the fresh window and find the contiguous matching run
	//    ending at the newest sample. Walk points in timestamp order so that
	//    out-of-order insertion cannot corrupt the run.
	points := make([]point, 0, len(rt.samples))
	for ts, v := range rt.samples {
		points = append(points, point{ts: ts, v: v})
	}
	sort.Slice(points, func(i, j int) bool { return points[i].ts < points[j].ts })

	hasData := len(points) > 0
	var newest point
	var runStart int64
	if hasData {
		newest = points[len(points)-1]
		st.LastTs = newest.ts
		st.LastValue = newest.v
		runStart = points[0].ts
		if !rt.rule.Op.match(newest.v, rt.rule.Threshold) {
			// Newest point breaks the run: no contiguous match at the tip.
			runStart = newest.ts + 1
		} else {
			for i := len(points) - 2; i >= 0; i-- {
				if !rt.rule.Op.match(points[i].v, rt.rule.Threshold) {
					runStart = points[i].ts + 1
					break
				}
			}
		}
	}

	// 3) Decide the next state. A firing rule remembers its run start and
	//    holds onto it across evaluations as the window slides; it is lost
	//    only when a non-matching point (or no data) appears, which is the
	//    recover path. This makes repeated evaluations and re-deliveries
	//    idempotent (no duplicate firing).
	var next AlertState
	continuous := hasData && runStart <= newest.ts
	switch {
	case !hasData:
		// Expired/no data: never keep acting on a stale value.
		if prev == StateFiring || prev == StatePending {
			next = StateRecovered
		} else {
			next = StateNoData
		}
	case !continuous:
		if prev == StateFiring {
			next = StateRecovered
		} else {
			next = StatePending // fresh data exists but condition is broken
		}
	default:
		if prev == StateFiring && st.FiringSince > 0 && st.FiringSince < runStart {
			// Preserve the earlier start still covered by the window.
			runStart = st.FiringSince
		}
		st.FiringSince = runStart
		if newest.ts-runStart >= rt.rule.ForMs {
			next = StateFiring
		} else {
			next = StatePending
		}
	}
	if !continuous {
		st.FiringSince = 0
	}

	ev := Event{RuleName: rt.rule.Name, From: st.State, To: next, AtMs: nowMs, Value: st.LastValue}
	if next != st.State {
		st.State = next
		st.ChangedAt = nowMs
		switch next {
		case StateFiring:
			st.FiredCount++
		case StateRecovered:
			st.RecoveredCount++
		}
	}
	return ev
}

// Status returns a copy of the current status of a rule without evaluating.
func (e *Engine) Status(name string) (RuleStatus, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rt, ok := e.rules[name]
	if !ok {
		return RuleStatus{}, false
	}
	return rt.status, true
}

// RuleNames returns registered rule names in sorted order.
func (e *Engine) RuleNames() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	names := make([]string, 0, len(e.rules))
	for n := range e.rules {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func cloneRule(r Rule) Rule {
	cp := r
	if r.Labels != nil {
		cp.Labels = make(map[string]string, len(r.Labels))
		for k, v := range r.Labels {
			cp.Labels[k] = v
		}
	}
	return cp
}
