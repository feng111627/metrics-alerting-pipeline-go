package alerting

import (
	"sync"
	"time"
)

// Sample 是一个带时间戳的样本。
type Sample struct {
	MetricName string
	Value      float64
	Timestamp  time.Time
}

// Transition 记录一次状态迁移。
type Transition struct {
	Rule  string
	From  State
	To    State
	At    time.Time
	Value float64 // 触发本次评估的有效样本值（无数据时为 0）
}

// Clock 提供可替换的时间源，保证测试可复现。
type Clock interface {
	Now() time.Time
}

// Evaluator 对单条规则做滑动窗口评估与状态迁移。
//
// 评估语义（在时刻 now）：
//   - 取窗口 (now-Window, now] 内时间戳最新的样本作为有效值；
//   - 该样本距今超过 StaleAfter，或窗口内无任何样本 → NoData；
//   - 有效值满足阈值条件且已连续满足满 PendingFor → Firing；
//   - 满足但时长不足 → Pending；
//   - 有数据但条件不满足 → Resolved（未触发/已恢复）。
//
// 样本去重与乱序：相同时间戳的样本只接受第一个；乱序样本按时间戳
// 插入有序缓冲，评估永远基于完整缓冲重算，因此重复或乱序到达不会
// 造成重复触发或漏报恢复。
type Evaluator struct {
	rule  Rule
	clock Clock

	mu           sync.Mutex
	samples      []Sample // 按时间戳升序
	seen         map[time.Time]struct{}
	state        State
	pendingSince time.Time // 条件开始持续满足的时刻（评估时钟）
}

// NewEvaluator 创建评估器。clock 为 nil 时使用系统时间。
func NewEvaluator(rule Rule, clock Clock) (*Evaluator, error) {
	if err := rule.Validate(); err != nil {
		return nil, err
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &Evaluator{
		rule:  rule,
		clock: clock,
		seen:  make(map[time.Time]struct{}),
		state: StateNoData,
	}, nil
}

// Ingest 写入一个样本；重复（同指标同时间戳）或属于其他指标的样本
// 会被拒绝。返回本次写入是否被接受。
func (e *Evaluator) Ingest(s Sample) bool {
	if s.MetricName != e.rule.MetricName {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, dup := e.seen[s.Timestamp]; dup {
		return false
	}
	e.seen[s.Timestamp] = struct{}{}
	// 按时间戳升序插入，容忍乱序到达。
	i := len(e.samples)
	for i > 0 && e.samples[i-1].Timestamp.After(s.Timestamp) {
		i--
	}
	e.samples = append(e.samples, Sample{})
	copy(e.samples[i+1:], e.samples[i:])
	e.samples[i] = s
	return true
}

// Evaluate 在给定时刻评估规则，返回当前状态；
// 若发生状态迁移，返回的 transitions 非空。
func (e *Evaluator) Evaluate(now time.Time) (State, []Transition) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// 丢弃滑出窗口的旧样本。
	cutoff := now.Add(-e.rule.Window)
	keep := 0
	for keep < len(e.samples) && !e.samples[keep].Timestamp.After(cutoff) {
		keep++
	}
	if keep > 0 {
		e.samples = append([]Sample(nil), e.samples[keep:]...)
	}

	var (
		hasData bool
		value   float64
	)
	if n := len(e.samples); n > 0 {
		latest := e.samples[n-1]
		// 过期判定：最新样本距今超过 StaleAfter 按无数据处理。
		if now.Sub(latest.Timestamp) <= e.rule.StaleAfter {
			hasData = true
			value = latest.Value
		}
	}

	var next State
	switch {
	case !hasData:
		next = StateNoData
	case e.rule.Op.compare(value, e.rule.Threshold):
		if e.pendingSince.IsZero() {
			e.pendingSince = now
		}
		if e.state == StateFiring || now.Sub(e.pendingSince) >= e.rule.PendingFor {
			next = StateFiring
		} else {
			next = StatePending
		}
	default:
		next = StateResolved
	}
	if next != StatePending && next != StateFiring {
		e.pendingSince = time.Time{}
	}
	var transitions []Transition
	if next != e.state {
		transitions = append(transitions, Transition{
			Rule:  e.rule.Name,
			From:  e.state,
			To:    next,
			At:    now,
			Value: value,
		})
		e.state = next
	}
	return e.state, transitions
}

// EvaluateNow 用时钟当前时刻评估。
func (e *Evaluator) EvaluateNow() (State, []Transition) {
	return e.Evaluate(e.clock.Now())
}

// State 返回当前状态。
func (e *Evaluator) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}
