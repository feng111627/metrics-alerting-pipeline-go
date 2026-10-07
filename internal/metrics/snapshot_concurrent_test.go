package metrics

import (
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentWriteAndSnapshot 业务：并发写入与并发快照暴露。
// 不变量（每次快照都必须成立）：
//  1. 所有序列都必须存在（本测试每个序列至少被写一次）；
//  2. 计数器值非负、单调不减（跨两次快照比较）；
//  3. 直方图累计桶单调不减，count == 末桶计数，sum 非负；
//  4. 序列总数不超过基数上限；
//  5. 快照是深拷贝，取走后注册表继续变化不影响旧快照。
func TestConcurrentWriteAndSnapshot(t *testing.T) {
	const (
		seriesN      = 20
		writerPerS   = 4
		incPerWriter = 200
		maxSeries    = seriesN
	)
	reg := newTestRegistry(t, RegistryConfig{
		MaxSeriesPerMetric: maxSeries,
		Policy:             PolicyReject,
	})
	c, err := reg.RegisterCounter("cc", "", []string{"s"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := reg.RegisterHistogramHandle("hh", "", []string{"s"}, []float64{1, 5, 10})
	if err != nil {
		t.Fatal(err)
	}
	labels := make([]Labels, seriesN)
	for i := range labels {
		labels[i] = MustLabels(map[string]string{"s": fmt.Sprintf("%02d", i)})
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// 先写一轮，保证全部序列存在。
	for i := 0; i < seriesN; i++ {
		if err := c.Inc(labels[i]); err != nil {
			t.Fatal(err)
		}
		if err := h.Observe(labels[i], 2); err != nil {
			t.Fatal(err)
		}
	}

	// 读者：持续暴露快照并校验一致性。
	var snapWG sync.WaitGroup
	snapWG.Add(1)
	go func() {
		defer snapWG.Done()
		prev := map[string]float64{}
		prevHist := map[string]int64{}
		for {
			select {
			case <-stop:
				return
			default:
			}
			snap := reg.Snapshot()
			byName := map[string]MetricSnapshot{}
			for _, m := range snap.Metrics {
				byName[m.Name] = m
			}
			cc, ok1 := byName["cc"]
			hh, ok2 := byName["hh"]
			if !ok1 || !ok2 || len(cc.Series) != seriesN || len(hh.Series) != seriesN {
				t.Errorf("快照不完整: cc=%v hh=%v", ok1, ok2)
				return
			}
			// 同刻一致性：每条序列的时间戳都 <= TakenAtMs。
			for _, s := range hh.Series {
				if s.Histogram == nil {
					t.Errorf("直方图快照缺失 Histogram 视图")
					return
				}
				if s.Histogram.LastUpdate > snap.TakenAtMs {
					t.Errorf("看到未来时间戳: %d > %d", s.Histogram.LastUpdate, snap.TakenAtMs)
					return
				}
			}
			for _, s := range cc.Series {
				k := s.Labels[0].Value
				if s.Value < 0 {
					t.Errorf("计数器出现负值: %s=%v", k, s.Value)
					return
				}
				if s.Value < prev[k] {
					t.Errorf("计数器倒退: %s %v -> %v（读到半更新？）", k, prev[k], s.Value)
					return
				}
				prev[k] = s.Value
			}
			for _, s := range hh.Series {
				k := s.Labels[0].Value
				hs := s.Histogram
				var running int64 = 0
				for _, b := range hs.Buckets {
					if b.Cumulative < running {
						t.Errorf("累计桶非单调: %s", k)
						return
					}
					running = b.Cumulative
				}
				if hs.Count != hs.Buckets[len(hs.Buckets)-1].Cumulative {
					t.Errorf("count=%d 与 +Inf 桶=%d 不一致（半更新？）",
						hs.Count, hs.Buckets[len(hs.Buckets)-1].Cumulative)
					return
				}
				if hs.Count < prevHist[k] {
					t.Errorf("直方图 count 倒退: %s", k)
					return
				}
				prevHist[k] = hs.Count
			}
		}
	}()

	// 写者：每个序列固定数量的 Inc / Observe。
	for w := 0; w < writerPerS; w++ {
		wg.Add(2)
		go func(id int) {
			defer wg.Done()
			for n := 0; n < incPerWriter; n++ {
				i := (id + n) % seriesN
				if err := c.Inc(labels[i]); err != nil {
					t.Errorf("counter 写入失败: %v", err)
					return
				}
			}
		}(w)
		go func(id int) {
			defer wg.Done()
			for n := 0; n < incPerWriter; n++ {
				i := (id*3 + n) % seriesN
				v := float64((id+n)%11 + 1)
				if err := h.Observe(labels[i], v); err != nil {
					t.Errorf("histogram 写入失败: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	snapWG.Wait()

	// 最终快照：精确的总量可复现。
	snap := reg.Snapshot()
	var total float64
	var totalObs int64
	for _, m := range snap.Metrics {
		switch m.Name {
		case "cc":
			for _, s := range m.Series {
				total += s.Value
			}
		case "hh":
			for _, s := range m.Series {
				totalObs += s.Histogram.Count
			}
		}
	}
	// 预写 1 轮(seriesN) + writerPerS 个写者各 incPerWriter 次。
	wantCount := float64(seriesN + writerPerS*incPerWriter)
	if total != wantCount {
		t.Fatalf("counter 总量=%v 期望 %v", total, wantCount)
	}
	if totalObs != int64(wantCount) {
		t.Fatalf("histogram 观测数=%d 期望 %d", totalObs, int64(wantCount))
	}

	t.Run("快照深拷贝不受后续写入影响", func(t *testing.T) {
		before := reg.Snapshot()
		beforeVal := before.Metrics[0].Series[0].Value
		_ = c.Add(labels[0], 1000)
		if before.Metrics[0].Series[0].Value != beforeVal {
			t.Fatal("旧快照被后续写入污染")
		}
	})
}
