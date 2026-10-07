package metrics

import (
	"errors"
	"testing"
)

// TestSeriesLimitReject 业务：标签组合达到上限后拒绝新序列。
func TestSeriesLimitReject(t *testing.T) {
	reg := newTestRegistry(t, RegistryConfig{MaxSeriesPerMetric: 3, Policy: PolicyReject})
	c := mustCounter(t, reg, "limited", []string{"id"})

	t.Run("前三个序列正常", func(t *testing.T) {
		for _, id := range []string{"1", "2", "3"} {
			if err := c.Inc(MustLabels(map[string]string{"id": id})); err != nil {
				t.Fatalf("id=%s: %v", id, err)
			}
		}
	})
	t.Run("第四个序列被拒绝且错误可区分", func(t *testing.T) {
		err := c.Inc(MustLabels(map[string]string{"id": "4"}))
		if !errors.Is(err, ErrSeriesLimit) {
			t.Fatalf("期望 ErrSeriesLimit，实际 %v", err)
		}
	})
	t.Run("已有序列仍可更新，数量保持有界", func(t *testing.T) {
		if err := c.Inc(MustLabels(map[string]string{"id": "2"})); err != nil {
			t.Fatal(err)
		}
		snap := reg.Snapshot()
		if len(snap.Metrics[0].Series) != 3 {
			t.Fatalf("序列数=%d，期望 3", len(snap.Metrics[0].Series))
		}
	})
	t.Run("不同指标各自独立计数", func(t *testing.T) {
		g := mustGauge(t, reg, "other", []string{"id"})
		for _, id := range []string{"1", "2", "3"} {
			if err := g.Set(MustLabels(map[string]string{"id": id}), 1); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// TestSeriesLimitEvictOldest 业务：达到上限后淘汰最久未更新序列。
func TestSeriesLimitEvictOldest(t *testing.T) {
	reg := newTestRegistry(t, RegistryConfig{MaxSeriesPerMetric: 2, Policy: PolicyEvictOldest})
	c := mustCounter(t, reg, "evict", []string{"id"})

	testClockNow = 100
	_ = c.Inc(MustLabels(map[string]string{"id": "a"}))
	testClockNow = 200
	_ = c.Inc(MustLabels(map[string]string{"id": "b"}))

	t.Run("第三个序列淘汰最旧的a", func(t *testing.T) {
		testClockNow = 300
		if err := c.Inc(MustLabels(map[string]string{"id": "c"})); err != nil {
			t.Fatal(err)
		}
		if v, _ := c.Get(MustLabels(map[string]string{"id": "a"})); v != 0 {
			t.Fatalf("a 应已被淘汰，读到 %v", v)
		}
		if v, _ := c.Get(MustLabels(map[string]string{"id": "b"})); v != 1 {
			t.Fatalf("b 应保留，读到 %v", v)
		}
	})
	t.Run("被淘汰槽位已重置，不携带旧值", func(t *testing.T) {
		if v, _ := c.Get(MustLabels(map[string]string{"id": "c"})); v != 1 {
			t.Fatalf("c 应为新序列值 1，读到 %v", v)
		}
	})
	t.Run("访问旧序列会按TTL无关方式重新创建", func(t *testing.T) {
		testClockNow = 400
		// a 现在重新出现：当前 b(200) 最旧，应淘汰 b。
		if err := c.Inc(MustLabels(map[string]string{"id": "a"})); err != nil {
			t.Fatal(err)
		}
		if v, _ := c.Get(MustLabels(map[string]string{"id": "b"})); v != 0 {
			t.Fatalf("b 应已淘汰，读到 %v", v)
		}
		if v, _ := c.Get(MustLabels(map[string]string{"id": "a"})); v != 1 {
			t.Fatalf("a 应重新创建，读到 %v", v)
		}
	})
}

// TestSeriesTTL 业务：超过 TTL 未更新的序列按过期处理。
func TestSeriesTTL(t *testing.T) {
	reg := newTestRegistry(t, RegistryConfig{SeriesTTL: 1000})
	c := mustCounter(t, reg, "ttl", []string{"id"})

	testClockNow = 5000
	_ = c.Inc(MustLabels(map[string]string{"id": "hot"}))
	_ = c.Inc(MustLabels(map[string]string{"id": "cold"}))
	testClockNow = 5500
	_ = c.Inc(MustLabels(map[string]string{"id": "hot"}))

	t.Run("TTL内两条都可见", func(t *testing.T) {
		if got := seriesCount(reg, "ttl"); got != 2 {
			t.Fatalf("序列数=%d", got)
		}
	})
	t.Run("超过TTL的cold被惰性清除且不进快照", func(t *testing.T) {
		testClockNow = 6001 // cold(5000) 已过期，hot(5500) 仍新鲜
		snap := reg.Snapshot()
		if len(snap.Metrics[0].Series) != 1 {
			t.Fatalf("快照中序列数=%d，期望 1", len(snap.Metrics[0].Series))
		}
		if snap.Metrics[0].Series[0].Labels[0].Value != "hot" {
			t.Fatalf("剩余序列应为 hot")
		}
		if v, _ := c.Get(MustLabels(map[string]string{"id": "cold"})); v != 0 {
			t.Fatalf("过期序列读取应为 0，实际 %v", v)
		}
	})
	t.Run("过期槽位被回收，新写入不触发上限", func(t *testing.T) {
		// 触发惰性回收后再写 cold，应能正常重建。
		if err := c.Inc(MustLabels(map[string]string{"id": "cold"})); err != nil {
			t.Fatalf("过期序列应可重建: %v", err)
		}
	})
	t.Run("无TTL配置时永不过期", func(t *testing.T) {
		reg2 := newTestRegistry(t, RegistryConfig{})
		g := mustGauge(t, reg2, "forever", nil)
		testClockNow = 1
		_ = g.Set(MustLabels(nil), 9)
		testClockNow = 1 << 40
		if v, _ := g.Get(MustLabels(nil)); v != 9 {
			t.Fatalf("got %v", v)
		}
	})
}

func seriesCount(r *Registry, metric string) int {
	for _, m := range r.Snapshot().Metrics {
		if m.Name == metric {
			return len(m.Series)
		}
	}
	return -1
}

// TestUnregister 业务：删除的指标与序列不得出现在快照。
func TestUnregister(t *testing.T) {
	reg := newTestRegistry(t, RegistryConfig{})
	c := mustCounter(t, reg, "doomed", nil)
	_ = c.Inc(MustLabels(nil))
	if !reg.Unregister("doomed") {
		t.Fatal("首次删除应返回 true")
	}
	if reg.Unregister("doomed") {
		t.Fatal("再次删除应返回 false")
	}
	if len(reg.Snapshot().Metrics) != 0 {
		t.Fatal("删除后快照不应包含该指标")
	}
	if err := c.Inc(MustLabels(nil)); !errors.Is(err, ErrMetricNotFound) {
		t.Fatalf("句柄再写应失败，实际 %v", err)
	}
}
