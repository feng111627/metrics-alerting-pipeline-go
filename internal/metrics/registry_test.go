package metrics

import (
	"errors"
	"testing"
)

// TestRegistryRegistration 业务：指标注册校验。
// 覆盖：重复注册、非法指标名、非法/重复标签名、标签数上限、未知类型、非法桶。
func TestRegistryRegistration(t *testing.T) {
	reg := newTestRegistry(t, RegistryConfig{MaxLabels: 2})

	cases := []struct {
		name    string
		do      func() error
		wantErr error
	}{
		{"正常注册计数器", func() error {
			_, err := reg.RegisterCounter("requests_total", "请求总数", []string{"path"})
			return err
		}, nil},
		{"重复注册同名指标被拒绝", func() error {
			return reg.Register("requests_total", "dup", TypeCounter, nil)
		}, ErrDuplicateMetric},
		{"非法指标名-空串", func() error {
			return reg.Register("", "x", TypeCounter, nil)
		}, ErrInvalidName},
		{"非法指标名-数字开头", func() error {
			return reg.Register("9bad", "x", TypeCounter, nil)
		}, ErrInvalidName},
		{"非法指标名-含横杠", func() error {
			return reg.Register("bad-name", "x", TypeCounter, nil)
		}, ErrInvalidName},
		{"非法标签名", func() error {
			return reg.Register("m1", "x", TypeGauge, []string{"bad-label"})
		}, ErrInvalidLabel},
		{"重复标签名", func() error {
			return reg.Register("m2", "x", TypeGauge, []string{"env", "env"})
		}, ErrDuplicateLabel},
		{"标签数超过上限", func() error {
			return reg.Register("m3", "x", TypeGauge, []string{"a", "b", "c"})
		}, ErrTooManyLabels},
		{"直方图桶未严格递增", func() error {
			return reg.RegisterHistogram("h1", "x", nil, []float64{1, 1, 2})
		}, ErrInvalidBuckets},
		{"直方图桶含NaN", func() error {
			return reg.RegisterHistogram("h2", "x", nil, []float64{1, nanVal()})
		}, ErrInvalidBuckets},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.do()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("期望成功，实际错误: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("期望错误包含 %v，实际: %v", tc.wantErr, err)
			}
		})
	}

	t.Run("注册后标签名被规范化为有序", func(t *testing.T) {
		c, err := reg.RegisterCounter("ordered_labels", "", []string{"zone", "app"})
		if err != nil {
			t.Fatal(err)
		}
		got := c.LabelNames()
		if got[0] != "app" || got[1] != "zone" {
			t.Fatalf("期望 [app zone]，实际 %v", got)
		}
	})

	t.Run("未注册指标更新返回未找到", func(t *testing.T) {
		c := &Counter{reg: reg, name: "ghost"}
		if err := c.Inc(MustLabels(nil)); !errors.Is(err, ErrMetricNotFound) {
			t.Fatalf("期望 ErrMetricNotFound，实际 %v", err)
		}
	})
}

// TestCounterSemantics 业务：计数器更新约束。
func TestCounterSemantics(t *testing.T) {
	reg := newTestRegistry(t, RegistryConfig{MaxLabels: 2})
	c := mustCounter(t, reg, "counter_sem", nil)

	t.Run("初始值为零", func(t *testing.T) {
		if v, _ := c.Get(MustLabels(nil)); v != 0 {
			t.Fatalf("got %v", v)
		}
	})
	t.Run("Inc与Add累加", func(t *testing.T) {
		if err := c.Inc(MustLabels(nil)); err != nil {
			t.Fatal(err)
		}
		if err := c.Add(MustLabels(nil), 4); err != nil {
			t.Fatal(err)
		}
		if v, _ := c.Get(MustLabels(nil)); v != 5 {
			t.Fatalf("got %v", v)
		}
	})
	t.Run("负数增量被拒绝", func(t *testing.T) {
		if err := c.Add(MustLabels(nil), -1); !errors.Is(err, ErrNegativeValue) {
			t.Fatalf("期望 ErrNegativeValue，实际 %v", err)
		}
		if v, _ := c.Get(MustLabels(nil)); v != 5 {
			t.Fatalf("拒绝后值不应变化，got %v", v)
		}
	})
}

// TestGaugeSemantics 业务：仪表可增可减。
func TestGaugeSemantics(t *testing.T) {
	reg := newTestRegistry(t, RegistryConfig{MaxLabels: 2})
	g := mustGauge(t, reg, "gauge_sem", nil)

	t.Run("Set覆盖", func(t *testing.T) {
		if err := g.Set(MustLabels(nil), 3.5); err != nil {
			t.Fatal(err)
		}
		if v, _ := g.Get(MustLabels(nil)); v != 3.5 {
			t.Fatalf("got %v", v)
		}
	})
	t.Run("Add支持负数", func(t *testing.T) {
		if err := g.Add(MustLabels(nil), -5); err != nil {
			t.Fatal(err)
		}
		if v, _ := g.Get(MustLabels(nil)); v != -1.5 {
			t.Fatalf("got %v", v)
		}
	})
	t.Run("Inc/Dec", func(t *testing.T) {
		_ = g.Inc(MustLabels(nil))
		_ = g.Dec(MustLabels(nil))
		_ = g.Dec(MustLabels(nil))
		if v, _ := g.Get(MustLabels(nil)); v != -2.5 {
			t.Fatalf("got %v", v)
		}
	})
}

// TestHistogramSemantics 业务：直方图桶累计与统计。
func TestHistogramSemantics(t *testing.T) {
	reg := newTestRegistry(t, RegistryConfig{MaxLabels: 2})
	h, err := reg.RegisterHistogramHandle("hist_sem", "", nil, []float64{1, 5, 10})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("桶边界自动追加+Inf", func(t *testing.T) {
		bounds := h.Buckets()
		if len(bounds) != 4 || bounds[3] != infVal() {
			t.Fatalf("bounds=%v", bounds)
		}
	})

	for _, v := range []float64{0.5, 2, 2, 9, 50} {
		if err := h.Observe(MustLabels(nil), v); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("累计桶计数/sum/count", func(t *testing.T) {
		view, err := h.Get(MustLabels(nil))
		if err != nil {
			t.Fatal(err)
		}
		want := []int64{1, 3, 4, 5}
		for i, w := range want {
			if view.Buckets[i].Cumulative != w {
				t.Fatalf("bucket %d 期望 %d 实际 %d", i, w, view.Buckets[i].Cumulative)
			}
		}
		if view.Count != 5 {
			t.Fatalf("count=%d", view.Count)
		}
		if view.Sum != 63.5 {
			t.Fatalf("sum=%v", view.Sum)
		}
	})
}

// TestLabelValidation 业务：标签集构造与键约束。
func TestLabelValidation(t *testing.T) {
	t.Run("map构造非法标签名", func(t *testing.T) {
		if _, err := NewLabels(map[string]string{"a-b": "1"}); !errors.Is(err, ErrInvalidLabel) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("pair构造重复键", func(t *testing.T) {
		_, err := NewLabelsFromPairs(
			LabelPair{Name: "app", Value: "1"},
			LabelPair{Name: "app", Value: "2"},
		)
		if !errors.Is(err, ErrDuplicateLabel) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("Key转义无冲突且有序", func(t *testing.T) {
		l1, _ := NewLabels(map[string]string{"a": "1,2", "b": "3"})
		l2, _ := NewLabels(map[string]string{"b": "3", "a": "1\\,2"})
		if l1.Key() == l2.Key() {
			t.Fatalf("转义后两个不同标签集冲突: %s", l1.Key())
		}
	})
	t.Run("更新时标签键集合必须与注册一致", func(t *testing.T) {
		reg := newTestRegistry(t, RegistryConfig{MaxLabels: 2})
		c := mustCounter(t, reg, "label_match", []string{"env"})
		err := c.Inc(MustLabels(map[string]string{"zone": "a"}))
		if !errors.Is(err, ErrLabelMismatch) {
			t.Fatalf("期望 ErrLabelMismatch，实际 %v", err)
		}
		err = c.Inc(MustLabels(map[string]string{"env": "prod", "zone": "a"}))
		if !errors.Is(err, ErrLabelMismatch) {
			t.Fatalf("多标签应拒绝，实际 %v", err)
		}
		if err := c.Inc(MustLabels(map[string]string{"env": "prod"})); err != nil {
			t.Fatalf("正确键集应成功: %v", err)
		}
	})
}
