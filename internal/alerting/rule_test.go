package alerting

import (
	"errors"
	"testing"
)

// TestRuleValidation 业务：告警规则参数校验，失败原因必须可区分。
func TestRuleValidation(t *testing.T) {
	cases := []struct {
		name    string
		rule    Rule
		wantErr error
	}{
		{"合法规则", Rule{Name: "r1", Metric: "cpu", Op: OpGt, Threshold: 80, WindowMs: 1000, ForMs: 500}, nil},
		{"For为零合法", Rule{Name: "r2", Metric: "cpu", Op: OpGt, Threshold: 1, WindowMs: 100}, nil},
		{"规则名为空", Rule{Name: "", Metric: "cpu", Op: OpGt, WindowMs: 1}, ErrInvalidRule},
		{"指标名为空", Rule{Name: "r", Metric: "", Op: OpGt, WindowMs: 1}, ErrInvalidRule},
		{"操作符非法", Rule{Name: "r", Metric: "cpu", Op: Operator(99), WindowMs: 1}, ErrInvalidRule},
		{"窗口非正", Rule{Name: "r", Metric: "cpu", Op: OpGt, WindowMs: 0}, ErrInvalidRule},
		{"For为负", Rule{Name: "r", Metric: "cpu", Op: OpGt, WindowMs: 100, ForMs: -1}, ErrInvalidRule},
		{"For超过窗口", Rule{Name: "r", Metric: "cpu", Op: OpGt, WindowMs: 100, ForMs: 101}, ErrInvalidRule},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.rule.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("期望合法，实际 %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("期望 %v，实际 %v", tc.wantErr, err)
			}
		})
	}
}

// TestEngineRuleLifecycle 业务：规则注册、重复注册、评估未注册规则。
func TestEngineRuleLifecycle(t *testing.T) {
	eng := NewEngine(EngineConfig{})

	t.Run("注册合法规则", func(t *testing.T) {
		if err := eng.AddRule(Rule{Name: "a", Metric: "m", Op: OpGt, Threshold: 1, WindowMs: 100}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("重复规则名被拒绝", func(t *testing.T) {
		err := eng.AddRule(Rule{Name: "a", Metric: "x", Op: OpLt, Threshold: 1, WindowMs: 100})
		if !errors.Is(err, ErrDuplicateRule) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("注册非法规则透传校验错误", func(t *testing.T) {
		if err := eng.AddRule(Rule{Name: "bad", Metric: "m"}); !errors.Is(err, ErrInvalidRule) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("初始状态为无数据", func(t *testing.T) {
		st, ok := eng.Status("a")
		if !ok {
			t.Fatal("规则应存在")
		}
		if st.State != StateNoData {
			t.Fatalf("got %s", st.State)
		}
	})
	t.Run("评估未注册规则返回未找到", func(t *testing.T) {
		if _, _, err := eng.Evaluate("ghost", 1); !errors.Is(err, ErrRuleNotFound) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("删除规则后状态不可查", func(t *testing.T) {
		if !eng.RemoveRule("a") {
			t.Fatal("删除应返回 true")
		}
		if eng.RemoveRule("a") {
			t.Fatal("重复删除应返回 false")
		}
		if _, ok := eng.Status("a"); ok {
			t.Fatal("删除后不应可查")
		}
	})
}

// TestOperators 业务：六种比较操作符的匹配结果。
func TestOperators(t *testing.T) {
	cases := []struct {
		op     Operator
		v, thr float64
		want   bool
	}{
		{OpGt, 5, 4, true}, {OpGt, 4, 4, false},
		{OpGe, 4, 4, true}, {OpGe, 3.9, 4, false},
		{OpLt, 3, 4, true}, {OpLt, 4, 4, false},
		{OpLe, 4, 4, true}, {OpLe, 4.1, 4, false},
		{OpEq, 4, 4, true}, {OpEq, 4.01, 4, false},
		{OpNe, 5, 4, true}, {OpNe, 4, 4, false},
	}
	for _, c := range cases {
		if got := c.op.match(c.v, c.thr); got != c.want {
			t.Errorf("%v.match(%v,%v)=%v want %v", c.op, c.v, c.thr, got, c.want)
		}
	}
}

// TestLabelSelector 业务：规则只接收匹配标签选择器的样本。
func TestLabelSelector(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	r := Rule{Name: "sel", Metric: "m", Labels: map[string]string{"env": "prod"}, Op: OpGt, Threshold: 10, WindowMs: 1000}
	if err := eng.AddRule(r); err != nil {
		t.Fatal(err)
	}
	eng.Ingest("m", map[string]string{"env": "dev"}, 1, 100)
	eng.Ingest("m", map[string]string{"env": "prod", "host": "a"}, 1, 100) // 额外标签允许
	st, _, _ := eng.Evaluate("sel", 1)
	if st.State == StateNoData {
		t.Fatal("prod 样本应被选择，dev 样本应被忽略")
	}
	if st.LastValue != 100 {
		t.Fatalf("被选样本值=%v", st.LastValue)
	}
}
