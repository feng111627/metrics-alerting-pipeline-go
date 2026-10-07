package alerting

import (
	"errors"
	"testing"
	"time"
)

var base = time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)

func testRule() Rule {
	return Rule{
		Name:       "high_latency",
		MetricName: "latency_p99",
		Window:     5 * time.Minute,
		PendingFor: 2 * time.Minute,
		StaleAfter: 3 * time.Minute,
		Op:         Gt,
		Threshold:  100,
	}
}

func ingest(t *testing.T, e *Evaluator, v float64, at time.Time) {
	t.Helper()
	if !e.Ingest(Sample{MetricName: "latency_p99", Value: v, Timestamp: at}) {
		t.Fatalf("样本(%v @ %v)应被接受", v, at)
	}
}

func TestRuleValidation(t *testing.T) {
	t.Log("业务: 告警规则校验 —— 非法规则被拒绝")
	r := testRule()
	if _, err := NewEvaluator(r, nil); err != nil {
		t.Fatalf("合法规则不应报错: %v", err)
	}
	bad := r
	bad.Window = 0
	if _, err := NewEvaluator(bad, nil); !errors.Is(err, ErrInvalidRule) {
		t.Fatalf("零窗口期望 ErrInvalidRule, 得到 %v", err)
	}
	bad = r
	bad.StaleAfter = -time.Second
	if _, err := NewEvaluator(bad, nil); !errors.Is(err, ErrInvalidRule) {
		t.Fatalf("负过期时间期望 ErrInvalidRule, 得到 %v", err)
	}
	bad = r
	bad.Name = ""
	if _, err := NewEvaluator(bad, nil); !errors.Is(err, ErrInvalidRule) {
		t.Fatalf("空规则名期望 ErrInvalidRule, 得到 %v", err)
	}
}

func TestStateMachineLifecycle(t *testing.T) {
	t.Log("业务: 告警状态机 —— NoData → Pending → Firing → Resolved 完整迁移")
	e, err := NewEvaluator(testRule(), nil)
	if err != nil {
		t.Fatal(err)
	}

	// 初始无数据。
	if st, _ := e.Evaluate(base); st != StateNoData {
		t.Fatalf("初始期望 NoData, 得到 %v", st)
	}

	// t=1min: 越限样本到达 → Pending。
	ingest(t, e, 150, base.Add(1*time.Minute))
	st, tr := e.Evaluate(base.Add(1 * time.Minute))
	if st != StatePending || len(tr) != 1 || tr[0].From != StateNoData || tr[0].To != StatePending {
		t.Fatalf("期望 NoData→Pending, 得到 state=%v transitions=%v", st, tr)
	}

	// t=2min: 条件持续满足但未满 PendingFor → 保持 Pending，无迁移。
	ingest(t, e, 160, base.Add(2*time.Minute))
	st, tr = e.Evaluate(base.Add(2 * time.Minute))
	if st != StatePending || len(tr) != 0 {
		t.Fatalf("期望保持 Pending 且无迁移, 得到 state=%v transitions=%v", st, tr)
	}

	// t=3min: 持续满足满 2min → Firing。
	ingest(t, e, 170, base.Add(3*time.Minute))
	st, tr = e.Evaluate(base.Add(3 * time.Minute))
	if st != StateFiring || len(tr) != 1 || tr[0].To != StateFiring {
		t.Fatalf("期望 Pending→Firing, 得到 state=%v transitions=%v", st, tr)
	}

	// t=4min: 条件仍满足 → 保持 Firing，不得重复触发。
	ingest(t, e, 180, base.Add(4*time.Minute))
	st, tr = e.Evaluate(base.Add(4 * time.Minute))
	if st != StateFiring || len(tr) != 0 {
		t.Fatalf("期望保持 Firing 且无重复触发, 得到 state=%v transitions=%v", st, tr)
	}

	// t=5min: 恢复正常值 → Resolved。
	ingest(t, e, 50, base.Add(5*time.Minute))
	st, tr = e.Evaluate(base.Add(5 * time.Minute))
	if st != StateResolved || len(tr) != 1 || tr[0].From != StateFiring || tr[0].To != StateResolved {
		t.Fatalf("期望 Firing→Resolved, 得到 state=%v transitions=%v", st, tr)
	}
	t.Log("用例结果: NoData→Pending→Firing→Resolved 迁移全部符合预期")
}

func TestStaleSeriesTreatedAsNoData(t *testing.T) {
	t.Log("业务: 序列过期 —— 超过 StaleAfter 无新样本按无数据判定，不沿用旧值")
	e, _ := NewEvaluator(testRule(), nil)

	ingest(t, e, 200, base)
	if st, _ := e.Evaluate(base); st != StatePending {
		t.Fatalf("越限样本期望 Pending, 得到 %v", st)
	}
	// 4 分钟无新样本，超过 StaleAfter=3min → NoData，而不是沿用旧值继续触发。
	st, tr := e.Evaluate(base.Add(4 * time.Minute))
	if st != StateNoData {
		t.Fatalf("过期后期望 NoData, 得到 %v", st)
	}
	if len(tr) != 1 || tr[0].To != StateNoData {
		t.Fatalf("期望迁移到 NoData, 得到 %v", tr)
	}
	// 再持续无数据，不得重复迁移。
	if _, tr := e.Evaluate(base.Add(10 * time.Minute)); len(tr) != 0 {
		t.Fatalf("持续无数据不应重复迁移, 得到 %v", tr)
	}
	t.Log("用例结果: 过期序列按无数据判定，旧值未被沿用")
}

func TestDuplicateAndOutOfOrderSamples(t *testing.T) {
	t.Log("业务: 样本去重与乱序 —— 重复样本不重复触发，乱序样本不漏报恢复")
	e, _ := NewEvaluator(testRule(), nil)

	s := Sample{MetricName: "latency_p99", Value: 150, Timestamp: base.Add(1 * time.Minute)}
	if !e.Ingest(s) {
		t.Fatal("首次写入应被接受")
	}
	if e.Ingest(s) {
		t.Fatal("重复样本应被拒绝")
	}
	if e.Ingest(Sample{MetricName: "other_metric", Value: 1, Timestamp: base}) {
		t.Fatal("其他指标的样本应被拒绝")
	}

	// 乱序补写：t=2min 的样本晚于 t=3min 到达。
	ingest(t, e, 160, base.Add(3*time.Minute))
	ingest(t, e, 155, base.Add(2*time.Minute))

	// t=3min 评估：条件自 t=1min 起持续满足（评估时钟首次见到条件满足在 t=3min）。
	st, _ := e.Evaluate(base.Add(3 * time.Minute))
	if st != StatePending {
		t.Fatalf("期望 Pending, 得到 %v", st)
	}
	// t=3min30s: 未满 PendingFor。
	st, tr := e.Evaluate(base.Add(3*time.Minute + 30*time.Second))
	if st != StatePending || len(tr) != 0 {
		t.Fatalf("期望保持 Pending, 得到 %v %v", st, tr)
	}
	// t=5min: 持续满足满 2min → Firing，且只触发一次。
	st, tr = e.Evaluate(base.Add(5 * time.Minute))
	if st != StateFiring || len(tr) != 1 || tr[0].To != StateFiring {
		t.Fatalf("期望恰好一次 Firing 迁移, 得到 state=%v transitions=%v", st, tr)
	}

	// 乱序到达的恢复样本（时间戳在过去但仍在窗口内）→ 恢复不得漏报。
	ingest(t, e, 80, base.Add(4*time.Minute+30*time.Second))
	st, tr = e.Evaluate(base.Add(5 * time.Minute))
	if st != StateResolved || len(tr) != 1 || tr[0].To != StateResolved {
		t.Fatalf("乱序恢复样本后期望 Firing→Resolved, 得到 state=%v transitions=%v", st, tr)
	}
	t.Log("用例结果: 重复样本被拒绝，乱序样本正确参与评估，无重复触发、无漏报恢复")
}

func TestWindowSliding(t *testing.T) {
	t.Log("业务: 滑动窗口 —— 滑出窗口的样本不再参与评估")
	rule := testRule()
	rule.PendingFor = 0 // 立即触发
	rule.StaleAfter = 10 * time.Minute
	e, _ := NewEvaluator(rule, nil)

	ingest(t, e, 200, base)
	st, _ := e.Evaluate(base)
	if st != StateFiring {
		t.Fatalf("期望立即 Firing, 得到 %v", st)
	}
	// 6 分钟后样本滑出 5min 窗口 → 无数据。
	st, tr := e.Evaluate(base.Add(6 * time.Minute))
	if st != StateNoData || len(tr) != 1 || tr[0].From != StateFiring {
		t.Fatalf("样本滑出窗口后期望 Firing→NoData, 得到 state=%v transitions=%v", st, tr)
	}
	t.Log("用例结果: 窗口滑动后旧样本被丢弃，状态正确回到 NoData")
}

func TestPendingResetOnConditionBreak(t *testing.T) {
	t.Log("业务: 待触发中断 —— 条件中断后重新计时，不得提前触发")
	e, _ := NewEvaluator(testRule(), nil)

	ingest(t, e, 150, base)
	if st, _ := e.Evaluate(base); st != StatePending {
		t.Fatalf("期望 Pending, 得到 %v", st)
	}
	// 1 分钟后条件中断。
	ingest(t, e, 50, base.Add(1*time.Minute))
	if st, tr := e.Evaluate(base.Add(1 * time.Minute)); st != StateResolved || len(tr) != 1 {
		t.Fatalf("条件中断期望 Pending→Resolved, 得到 %v %v", st, tr)
	}
	// 再次越限，重新计时：1 分钟后不应触发。
	ingest(t, e, 150, base.Add(2*time.Minute))
	e.Evaluate(base.Add(2 * time.Minute))
	st, _ := e.Evaluate(base.Add(3 * time.Minute))
	if st != StatePending {
		t.Fatalf("重新计时不应提前触发, 得到 %v", st)
	}
	// 满 2 分钟后触发。
	st, tr := e.Evaluate(base.Add(4 * time.Minute))
	if st != StateFiring || len(tr) != 1 {
		t.Fatalf("重新满足满 PendingFor 后期望 Firing, 得到 %v %v", st, tr)
	}
	t.Log("用例结果: 条件中断后 PendingFor 重新计时")
}
