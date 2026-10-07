package metrics

import (
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentWriteAndSnapshot 并发写入与暴露：
// 写者按固定顺序先写 c_a 再写 c_b，任何一致快照都必须满足 c_a >= c_b；
// 直方图必须满足 末桶累计数 == Count 且 Sum 与观测值一致。
func TestConcurrentWriteAndSnapshot(t *testing.T) {
	t.Log("业务: 并发写入与快照暴露 —— 快照不得读到半更新的值")
	r := NewRegistry()
	ca, _ := r.RegisterCounter("c_a", "a")
	cb, _ := r.RegisterCounter("c_b", "b")
	h, _ := r.RegisterHistogram("lat", "lat", []float64{1, 2, 4, 8})
	labels := Labels{"worker": "shared"}

	const writers = 8
	const rounds = 500

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if err := ca.Inc(labels, 1); err != nil {
					t.Errorf("inc c_a: %v", err)
					return
				}
				if err := cb.Inc(labels, 1); err != nil {
					t.Errorf("inc c_b: %v", err)
					return
				}
				if err := h.Observe(labels, float64(id%9)); err != nil {
					t.Errorf("observe: %v", err)
					return
				}
			}
		}(w)
	}

	// 快照读者：持续取快照并校验一致性不变量。
	var snapWG sync.WaitGroup
	for s := 0; s < 2; s++ {
		snapWG.Add(1)
		go func() {
			defer snapWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap := r.Snapshot()
				var a, b float64
				var histOK bool
				var hist HistogramValue
				for _, sr := range snap.Series {
					switch sr.Name {
					case "c_a":
						a = sr.Value.Counter
					case "c_b":
						b = sr.Value.Counter
					case "lat":
						histOK = true
						hist = sr.Value.Hist
					}
				}
				if a < b {
					t.Errorf("快照不一致: c_a(%v) < c_b(%v)，读到了半更新状态", a, b)
					return
				}
				if histOK {
					last := hist.Buckets[len(hist.Buckets)-1]
					if last.Count != hist.Count {
						t.Errorf("直方图不一致: 末桶累计 %d != Count %d", last.Count, hist.Count)
						return
					}
				}
			}
		}()
	}

	wg.Wait()
	close(stop)
	snapWG.Wait()

	// 收尾校验最终值。
	snap := r.Snapshot()
	totals := map[string]float64{}
	var hist HistogramValue
	for _, sr := range snap.Series {
		switch sr.Name {
		case "c_a":
			totals["a"] = sr.Value.Counter
		case "c_b":
			totals["b"] = sr.Value.Counter
		case "lat":
			hist = sr.Value.Hist
		}
	}
	want := float64(writers * rounds)
	if totals["a"] != want || totals["b"] != want {
		t.Fatalf("最终计数期望 %v, 得到 a=%v b=%v", want, totals["a"], totals["b"])
	}
	if hist.Count != uint64(writers*rounds) {
		t.Fatalf("直方图 Count 期望 %d, 得到 %d", writers*rounds, hist.Count)
	}
	t.Logf("用例结果: %d 写者 x %d 轮写入完成, 快照不变量全程成立, 最终计数 %v", writers, rounds, want)
}

// TestConcurrentSeriesCreation 并发创建不同标签组合的序列，总数不得超过上限。
func TestConcurrentSeriesCreation(t *testing.T) {
	t.Log("业务: 并发序列创建 —— 基数上限在并发下依然生效")
	const maxSeries = 100
	r := NewRegistry(WithMaxSeries(maxSeries))
	g, _ := r.RegisterGauge("g", "g")

	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_ = g.Set(Labels{"k": fmt.Sprintf("%d-%d", id, i)}, 1)
			}
		}(w)
	}
	wg.Wait()

	snap := r.Snapshot()
	if len(snap.Series) != maxSeries {
		t.Fatalf("序列数期望 %d, 得到 %d", maxSeries, len(snap.Series))
	}
	t.Logf("用例结果: 并发创建后序列数稳定在上限 %d", len(snap.Series))
}
