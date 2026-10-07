package metrics

import (
	"errors"
	"testing"
)

func TestRegistrationValidation(t *testing.T) {
	t.Log("业务: 指标注册 —— 重复注册、非法名称、非法直方图桶均需给出可区分错误")
	r := NewRegistry()

	if _, err := r.RegisterCounter("requests_total", "requests"); err != nil {
		t.Fatalf("注册计数器失败: %v", err)
	}
	t.Run("重复注册被拒绝", func(t *testing.T) {
		_, err := r.RegisterCounter("requests_total", "dup")
		if !errors.Is(err, ErrDuplicateRegistration) {
			t.Fatalf("期望 ErrDuplicateRegistration, 得到 %v", err)
		}
		// 同名不同类型同样视为重复注册
		_, err = r.RegisterGauge("requests_total", "dup")
		if !errors.Is(err, ErrDuplicateRegistration) {
			t.Fatalf("期望 ErrDuplicateRegistration, 得到 %v", err)
		}
	})
	t.Run("非法名称被拒绝", func(t *testing.T) {
		for _, name := range []string{"", "9bad", "has-dash", "中文名"} {
			if _, err := r.RegisterCounter(name, "h"); !errors.Is(err, ErrInvalidName) {
				t.Fatalf("名称 %q 期望 ErrInvalidName, 得到 %v", name, err)
			}
		}
	})
	t.Run("空帮助文本被拒绝", func(t *testing.T) {
		if _, err := r.RegisterGauge("g1", ""); !errors.Is(err, ErrConstraintViolation) {
			t.Fatalf("期望 ErrConstraintViolation, 得到 %v", err)
		}
	})
	t.Run("非法直方图桶被拒绝", func(t *testing.T) {
		if _, err := r.RegisterHistogram("h1", "h", nil); !errors.Is(err, ErrConstraintViolation) {
			t.Fatalf("空桶期望 ErrConstraintViolation, 得到 %v", err)
		}
		if _, err := r.RegisterHistogram("h2", "h", []float64{2, 1}); !errors.Is(err, ErrConstraintViolation) {
			t.Fatalf("非递增桶期望 ErrConstraintViolation, 得到 %v", err)
		}
	})
}

func TestUpdateConstraints(t *testing.T) {
	t.Log("业务: 指标更新 —— 非法标签与违反类型约束的更新被拒绝")
	r := NewRegistry()
	c, _ := r.RegisterCounter("c_total", "c")
	g, _ := r.RegisterGauge("g", "g")
	h, _ := r.RegisterHistogram("h", "h", []float64{1, 5, 10})

	t.Run("非法标签被拒绝", func(t *testing.T) {
		bad := Labels{"bad-key": "v"}
		if err := c.Inc(bad, 1); !errors.Is(err, ErrInvalidLabel) {
			t.Fatalf("期望 ErrInvalidLabel, 得到 %v", err)
		}
		if err := g.Set(bad, 1); !errors.Is(err, ErrInvalidLabel) {
			t.Fatalf("期望 ErrInvalidLabel, 得到 %v", err)
		}
		if err := h.Observe(bad, 1); !errors.Is(err, ErrInvalidLabel) {
			t.Fatalf("期望 ErrInvalidLabel, 得到 %v", err)
		}
	})
	t.Run("计数器负增量被拒绝", func(t *testing.T) {
		if err := c.Inc(nil, -1); !errors.Is(err, ErrConstraintViolation) {
			t.Fatalf("期望 ErrConstraintViolation, 得到 %v", err)
		}
	})
	t.Run("正常更新与快照值", func(t *testing.T) {
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatalf("更新失败: %v", err)
			}
		}
		must(c.Inc(Labels{"path": "/a"}, 2))
		must(c.Inc(Labels{"path": "/a"}, 3))
		must(g.Set(Labels{"path": "/a"}, 7))
		must(g.Add(Labels{"path": "/a"}, -2))
		for _, v := range []float64{0.5, 3, 7, 12} {
			must(h.Observe(Labels{"path": "/a"}, v))
		}
		snap := r.Snapshot()
		if len(snap.Series) != 3 {
			t.Fatalf("期望 3 条序列, 得到 %d", len(snap.Series))
		}
		byName := map[string]Value{}
		for _, s := range snap.Series {
			byName[s.Name] = s.Value
		}
		if byName["c_total"].Counter != 5 {
			t.Fatalf("计数器期望 5, 得到 %v", byName["c_total"].Counter)
		}
		if byName["g"].Gauge != 5 {
			t.Fatalf("仪表期望 5, 得到 %v", byName["g"].Gauge)
		}
		hv := byName["h"].Hist
		if hv.Count != 4 || hv.Sum != 22.5 {
			t.Fatalf("直方图期望 count=4 sum=22.5, 得到 count=%d sum=%v", hv.Count, hv.Sum)
		}
		wantCumulative := []uint64{1, 2, 3, 4} // le=1, le=5, le=10, +Inf
		for i, b := range hv.Buckets {
			if b.Count != wantCumulative[i] {
				t.Fatalf("桶 %d 期望累计 %d, 得到 %d", i, wantCumulative[i], b.Count)
			}
		}
	})
}

func TestCardinalityLimit(t *testing.T) {
	t.Log("业务: 标签基数上限 —— 达到上限后拒绝新序列但不影响已有序列更新")
	r := NewRegistry(WithMaxSeries(2))
	g, _ := r.RegisterGauge("g", "g")

	if err := g.Set(Labels{"id": "1"}, 1); err != nil {
		t.Fatalf("第 1 条序列应创建成功: %v", err)
	}
	if err := g.Set(Labels{"id": "2"}, 2); err != nil {
		t.Fatalf("第 2 条序列应创建成功: %v", err)
	}
	if err := g.Set(Labels{"id": "3"}, 3); !errors.Is(err, ErrCardinalityLimit) {
		t.Fatalf("超限期望 ErrCardinalityLimit, 得到 %v", err)
	}
	// 已有序列仍可更新
	if err := g.Set(Labels{"id": "1"}, 100); err != nil {
		t.Fatalf("已有序列更新不应受限: %v", err)
	}
	// 删除后名额释放
	if !r.Delete("g", Labels{"id": "2"}) {
		t.Fatal("删除已存在序列应返回 true")
	}
	if err := g.Set(Labels{"id": "3"}, 3); err != nil {
		t.Fatalf("删除后应能创建新序列: %v", err)
	}
	if r.Delete("g", Labels{"id": "2"}) {
		t.Fatal("重复删除应返回 false")
	}
}

func TestDeletedSeriesAbsentFromSnapshot(t *testing.T) {
	t.Log("业务: 快照 —— 已删除序列不得出现在快照中")
	r := NewRegistry()
	g, _ := r.RegisterGauge("g", "g")
	_ = g.Set(Labels{"id": "1"}, 1)
	_ = g.Set(Labels{"id": "2"}, 2)
	r.Delete("g", Labels{"id": "1"})

	snap := r.Snapshot()
	if len(snap.Series) != 1 {
		t.Fatalf("期望 1 条序列, 得到 %d", len(snap.Series))
	}
	if snap.Series[0].Labels["id"] != "1" && snap.Series[0].Labels["id"] != "2" {
		t.Fatalf("未知标签: %v", snap.Series[0].Labels)
	}
	if snap.Series[0].Labels["id"] == "1" {
		t.Fatal("已删除序列出现在快照中")
	}
}
