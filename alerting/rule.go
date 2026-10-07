package alerting

import (
	"errors"
	"fmt"
	"time"
)

// ErrInvalidRule 表示规则参数不合法。
var ErrInvalidRule = errors.New("alerting: invalid rule")

// State 是告警规则的状态机状态。
type State int

const (
	StateNoData   State = iota // 窗口内无有效数据（含序列过期）
	StatePending               // 条件已满足但未持续满 PendingFor
	StateFiring                // 已触发
	StateResolved              // 曾触发，条件已不再满足（恢复）
)

func (s State) String() string {
	switch s {
	case StateNoData:
		return "NoData"
	case StatePending:
		return "Pending"
	case StateFiring:
		return "Firing"
	case StateResolved:
		return "Resolved"
	}
	return "Unknown"
}

// Op 是比较运算符（样本值 op 阈值）。
type Op int

const (
	Gt Op = iota
	Ge
	Lt
	Le
)

func (op Op) compare(v, threshold float64) bool {
	switch op {
	case Gt:
		return v > threshold
	case Ge:
		return v >= threshold
	case Lt:
		return v < threshold
	case Le:
		return v <= threshold
	}
	return false
}

// Rule 描述一条基于滑动窗口的告警规则。
//
// 语义：在窗口 (now-Window, now] 内取最新一个有效样本，与阈值比较；
// 条件连续满足满 PendingFor 才进入 Firing；条件不再满足立即恢复（Resolved）；
// 最新样本距今超过 StaleAfter 则按无数据（NoData）判定，不沿用旧值。
type Rule struct {
	Name       string
	MetricName string
	Window     time.Duration // 滑动窗口长度，必须 > 0
	PendingFor time.Duration // 条件需持续满足的时长，0 表示立即触发
	StaleAfter time.Duration // 样本超过该时长未更新视为过期，必须 > 0
	Op         Op
	Threshold  float64
}

// Validate 校验规则参数。
func (r Rule) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("%w: name must not be empty", ErrInvalidRule)
	}
	if r.MetricName == "" {
		return fmt.Errorf("%w: metric name must not be empty", ErrInvalidRule)
	}
	if r.Window <= 0 {
		return fmt.Errorf("%w: window must be > 0", ErrInvalidRule)
	}
	if r.PendingFor < 0 {
		return fmt.Errorf("%w: pending-for must be >= 0", ErrInvalidRule)
	}
	if r.StaleAfter <= 0 {
		return fmt.Errorf("%w: stale-after must be > 0", ErrInvalidRule)
	}
	if r.Op < Gt || r.Op > Le {
		return fmt.Errorf("%w: unknown comparison op", ErrInvalidRule)
	}
	return nil
}
