package alerting

import "testing"

func eval(t *testing.T, e *Engine, name string, now int64) RuleStatus {
	t.Helper()
	st, _, err := e.Evaluate(name, now)
	if err != nil {
		t.Fatalf("eval %s: %v", name, err)
	}
	return st
}

func firingRule() Rule {
	return Rule{Name: "cpu_high", Metric: "cpu", Op: OpGt, Threshold: 80, WindowMs: 1000, ForMs: 500}
}

// TestStateMachineFullLifecycle 业务：无数据→待触发→已触发→条件恢复→待触发 的完整路径。
func TestStateMachineFullLifecycle(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	if err := eng.AddRule(firingRule()); err != nil {
		t.Fatal(err)
	}

	t.Run("无数据", func(t *testing.T) {
		st := eval(t, eng, "cpu_high", 0)
		if st.State != StateNoData {
			t.Fatalf("got %s", st.State)
		}
	})
	t.Run("首个满足样本进入待触发", func(t *testing.T) {
		eng.Ingest("cpu", nil, 0, 90)
		st := eval(t, eng, "cpu_high", 0)
		if st.State != StatePending {
			t.Fatalf("got %s", st.State)
		}
	})
	t.Run("持续时间不足仍待触发", func(t *testing.T) {
		eng.Ingest("cpu", nil, 400, 90)
		st := eval(t, eng, "cpu_high", 400)
		if st.State != StatePending {
			t.Fatalf("got %s", st.State)
		}
	})
	t.Run("持续满足达到For后触发一次", func(t *testing.T) {
		eng.Ingest("cpu", nil, 500, 90)
		st := eval(t, eng, "cpu_high", 500)
		if st.State != StateFiring {
			t.Fatalf("got %s", st.State)
		}
		if st.FiredCount != 1 {
			t.Fatalf("FiredCount=%d", st.FiredCount)
		}
	})
	t.Run("已触发期间重复评估不重复触发", func(t *testing.T) {
		for _, ts := range []int64{500, 600, 999} {
			if ts != 500 {
				eng.Ingest("cpu", nil, ts, 90)
			}
			st := eval(t, eng, "cpu_high", ts)
			if st.State != StateFiring || st.FiredCount != 1 {
				t.Fatalf("ts=%d state=%s fired=%d", ts, st.State, st.FiredCount)
			}
		}
	})
	t.Run("条件不再满足进入恢复（一次性）", func(t *testing.T) {
		eng.Ingest("cpu", nil, 1000, 50)
		st := eval(t, eng, "cpu_high", 1000)
		if st.State != StateRecovered {
			t.Fatalf("got %s", st.State)
		}
		if st.RecoveredCount != 1 {
			t.Fatalf("RecoveredCount=%d", st.RecoveredCount)
		}
	})
	t.Run("恢复后下一评估回落到待触发", func(t *testing.T) {
		st := eval(t, eng, "cpu_high", 1000)
		if st.State != StatePending {
			t.Fatalf("got %s", st.State)
		}
	})
	t.Run("恢复后可再次触发", func(t *testing.T) {
		eng.Ingest("cpu", nil, 1200, 90)
		eng.Ingest("cpu", nil, 1800, 90)
		st := eval(t, eng, "cpu_high", 1800)
		if st.State != StateFiring {
			t.Fatalf("got %s", st.State)
		}
		if st.FiredCount != 2 {
			t.Fatalf("FiredCount=%d 期望 2", st.FiredCount)
		}
	})
}

// TestNoDataRecovery 业务：触发后样本过期，按无数据判定并产出恢复，而非沿用旧值。
func TestNoDataRecovery(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	if err := eng.AddRule(firingRule()); err != nil {
		t.Fatal(err)
	}
	eng.Ingest("cpu", nil, 1000, 90)
	eng.Ingest("cpu", nil, 1500, 90)
	if st := eval(t, eng, "cpu_high", 1500); st.State != StateFiring {
		t.Fatalf("前置触发失败: %s", st.State)
	}

	t.Run("窗口边界内仍触发", func(t *testing.T) {
		// 最旧点 1000，窗口 [1501-1000=501, now]：1000 已过期但 1500 新鲜。
		st := eval(t, eng, "cpu_high", 2000)
		if st.State != StateFiring {
			t.Fatalf("got %s", st.State)
		}
	})
	t.Run("最后一点滑出窗口后按无数据恢复", func(t *testing.T) {
		// now=2501，cutoff=1501，样本 1500 <= cutoff 已过期。
		st, ev, _ := eng.Evaluate("cpu_high", 2501)
		if st.State != StateRecovered || ev.To != StateRecovered {
			t.Fatalf("got %s event=%v", st.State, ev)
		}
	})
	t.Run("再评估稳定在无数据，不重复恢复", func(t *testing.T) {
		st := eval(t, eng, "cpu_high", 2501)
		if st.State != StateNoData {
			t.Fatalf("got %s", st.State)
		}
		if st.RecoveredCount != 1 {
			t.Fatalf("RecoveredCount=%d", st.RecoveredCount)
		}
	})
	t.Run("待触发状态下断流也走恢复", func(t *testing.T) {
		eng2 := NewEngine(EngineConfig{})
		_ = eng2.AddRule(firingRule())
		eng2.Ingest("cpu", nil, 0, 90) // 只待触发
		_ = eval(t, eng2, "cpu_high", 0)
		st, _, _ := eng2.Evaluate("cpu_high", 1001)
		if st.State != StateRecovered {
			t.Fatalf("got %s", st.State)
		}
	})
}

// TestDuplicateAndOutOfOrder 业务：重复到达与乱序到达不能重复触发或漏报恢复。
func TestDuplicateAndOutOfOrder(t *testing.T) {
	t.Run("重复时间戳只算一个点", func(t *testing.T) {
		eng := NewEngine(EngineConfig{})
		_ = eng.AddRule(Rule{Name: "r", Metric: "m", Op: OpGt, Threshold: 10, WindowMs: 1000, ForMs: 100})
		eng.Ingest("m", nil, 100, 20)
		eng.Ingest("m", nil, 100, 20) // 重复投递
		eng.Ingest("m", nil, 100, 20)
		st := eval(t, eng, "r", 100)
		if st.State != StatePending { // 只有一个时间点，span=0 < ForMs
			t.Fatalf("重复点导致时长虚增: %s", st.State)
		}
	})
	t.Run("乱序补点能提前触发", func(t *testing.T) {
		eng := NewEngine(EngineConfig{})
		_ = eng.AddRule(Rule{Name: "r", Metric: "m", Op: OpGt, Threshold: 10, WindowMs: 1000, ForMs: 300})
		// 先到 500 的点（待触发），再迟到 200 的点。
		eng.Ingest("m", nil, 500, 20)
		if st := eval(t, eng, "r", 500); st.State != StatePending {
			t.Fatalf("got %s", st.State)
		}
		eng.Ingest("m", nil, 200, 20)
		st := eval(t, eng, "r", 500)
		if st.State != StateFiring { // span 300 >= For
			t.Fatalf("乱序补点应满足持续时间: %s", st.State)
		}
		if st.FiredCount != 1 {
			t.Fatalf("FiredCount=%d", st.FiredCount)
		}
	})
	t.Run("乱序到达的违例点必须漏报不了恢复", func(t *testing.T) {
		eng := NewEngine(EngineConfig{})
		_ = eng.AddRule(Rule{Name: "r", Metric: "m", Op: OpGt, Threshold: 10, WindowMs: 1000, ForMs: 0})
		eng.Ingest("m", nil, 300, 20)
		eng.Ingest("m", nil, 400, 20)
		if st := eval(t, eng, "r", 400); st.State != StateFiring {
			t.Fatalf("前置: %s", st.State)
		}
		// 迟到一个窗口内的违例点，且不是最新点——连续区间被打断到该点之后。
		eng.Ingest("m", nil, 200, 5)
		st := eval(t, eng, "r", 400)
		if st.State != StateFiring {
			// 违例点在区间起点之前，runStart=201，span=199，For=0仍触发。
			t.Fatalf("For=0 时违例旧点不应影响末端连续区间，got %s", st.State)
		}
		// 违例点作为最新点到达 -> 必须恢复。
		eng.Ingest("m", nil, 401, 5)
		st = eval(t, eng, "r", 401)
		if st.State != StateRecovered {
			t.Fatalf("最新点违例必须恢复，got %s", st.State)
		}
	})
	t.Run("同时间戳后到的不同值覆盖旧值", func(t *testing.T) {
		eng := NewEngine(EngineConfig{})
		_ = eng.AddRule(Rule{Name: "r", Metric: "m", Op: OpGt, Threshold: 10, WindowMs: 1000})
		eng.Ingest("m", nil, 100, 20)
		eng.Ingest("m", nil, 100, 5) // 同 ts 纠偏
		st := eval(t, eng, "r", 100)
		if st.State != StatePending {
			t.Fatalf("覆盖后最新值 5 不应满足 >10，got %s", st.State)
		}
	})
}

// TestForZeroFiresImmediately 业务：For=0 时单个满足样本立即触发。
func TestForZeroFiresImmediately(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	_ = eng.AddRule(Rule{Name: "r", Metric: "m", Op: OpLt, Threshold: 1, WindowMs: 1000})
	eng.Ingest("m", nil, 100, 0.5)
	st := eval(t, eng, "r", 100)
	if st.State != StateFiring {
		t.Fatalf("got %s", st.State)
	}
}

// TestEvaluateAll 业务：同一逻辑时刻评估全部规则，状态彼此独立。
func TestEvaluateAll(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	_ = eng.AddRule(Rule{Name: "a", Metric: "m1", Op: OpGt, Threshold: 10, WindowMs: 1000})
	_ = eng.AddRule(Rule{Name: "b", Metric: "m2", Op: OpGt, Threshold: 10, WindowMs: 1000})
	eng.Ingest("m1", nil, 0, 20)
	statuses, events := eng.EvaluateAll(0)
	if len(statuses) != 2 {
		t.Fatalf("statuses=%d", len(statuses))
	}
	if statuses[0].Rule.Name != "a" || statuses[0].State != StateFiring {
		t.Fatalf("a 应触发: %+v", statuses[0])
	}
	if statuses[1].State != StateNoData {
		t.Fatalf("b 应无数据: %s", statuses[1].State)
	}
	firingEvents := 0
	for _, ev := range events {
		if ev.To == StateFiring {
			firingEvents++
		}
	}
	if firingEvents != 1 {
		t.Fatalf("firing events=%d", firingEvents)
	}
}
