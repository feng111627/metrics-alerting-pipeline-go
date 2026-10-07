package alerting

import (
	"sync"
	"testing"

	"github.com/feng111627/metrics-alerting-pipeline-go/internal/metrics"
)

// TestRegistrySnapshotIntegration 业务：注册表快照喂给引擎评估，
// 序列 TTL 过期后引擎在下一窗口按无数据处理。
func TestRegistrySnapshotIntegration(t *testing.T) {
	var now int64 = 1000
	reg := metrics.NewRegistry(metrics.RegistryConfig{
		SeriesTTL: 2000,
		NowMs:     func() int64 { return now },
	})
	eng := NewEngine(EngineConfig{NowMs: func() int64 { return now }})

	cpu, err := reg.RegisterGauge("cpu_usage", "cpu", []string{"host"})
	if err != nil {
		t.Fatal(err)
	}
	rule := Rule{
		Name:      "cpu_spike",
		Metric:    "cpu_usage",
		Labels:    map[string]string{"host": "db-1"},
		Op:        OpGt,
		Threshold: 80,
		WindowMs:  3000,
		ForMs:     0,
	}
	if err := eng.AddRule(rule); err != nil {
		t.Fatal(err)
	}

	t.Run("正常序列触发", func(t *testing.T) {
		_ = cpu.Set(metrics.MustLabels(map[string]string{"host": "db-1"}), 95)
		_ = cpu.Set(metrics.MustLabels(map[string]string{"host": "web-1"}), 10)
		eng.IngestSnapshot(reg.Snapshot())
		st, ev, _ := eng.Evaluate("cpu_spike", now)
		if st.State != StateFiring || ev.To != StateFiring {
			t.Fatalf("got %s", st.State)
		}
	})
	t.Run("序列仍新鲜且数值正常时恢复", func(t *testing.T) {
		now = 2000
		_ = cpu.Set(metrics.MustLabels(map[string]string{"host": "db-1"}), 20)
		eng.IngestSnapshot(reg.Snapshot())
		st, ev, _ := eng.Evaluate("cpu_spike", now)
		if st.State != StateRecovered || ev.To != StateRecovered {
			t.Fatalf("got %s", st.State)
		}
	})
	t.Run("再次升高重新触发", func(t *testing.T) {
		now = 2500
		_ = cpu.Set(metrics.MustLabels(map[string]string{"host": "db-1"}), 99)
		eng.IngestSnapshot(reg.Snapshot())
		st, _, _ := eng.Evaluate("cpu_spike", now)
		if st.State != StateFiring {
			t.Fatalf("got %s", st.State)
		}
	})
	t.Run("注册表侧TTL过期后快照无该序列，引擎窗口也过期→无数据恢复", func(t *testing.T) {
		// db-1 最后更新 2500，TTL 2000：now=4501 时已过期。
		now = 4501
		_ = cpu.Set(metrics.MustLabels(map[string]string{"host": "web-1"}), 50)
		snap := reg.Snapshot()
		for _, m := range snap.Metrics {
			for _, s := range m.Series {
				if s.Labels[0].Value == "db-1" {
					t.Fatal("过期序列 db-1 不应出现在快照")
				}
			}
		}
		eng.IngestSnapshot(snap)
		// 引擎窗口 3000：最后一点 2500，cutoff=4501-3000=1501；
		// 2500 仍在窗口！所以再推进到 5502 才真正过期。
		now = 5502
		st, ev, _ := eng.Evaluate("cpu_spike", now)
		if st.State != StateRecovered || ev.To != StateRecovered {
			t.Fatalf("got %s", st.State)
		}
		st = eval(t, eng, "cpu_spike", now)
		if st.State != StateNoData {
			t.Fatalf("got %s", st.State)
		}
	})
}

// TestConcurrentIngestEvaluate 业务：并发喂样本与并发评估，状态迁移计数稳定。
func TestConcurrentIngestEvaluate(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	const ruleN = 4
	for i := 0; i < ruleN; i++ {
		err := eng.AddRule(Rule{
			Name:      "r" + string(rune('0'+i)),
			Metric:    "m",
			Labels:    map[string]string{"k": string(rune('0' + i))},
			Op:        OpGt,
			Threshold: 50,
			WindowMs:  10000,
			ForMs:     100,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	stop := make(chan struct{})
	var writerWG, readerWG sync.WaitGroup
	// 时间戳分桶，保证同 ts 重复写覆盖等价、不同 ts 单调。
	for w := 0; w < 4; w++ {
		writerWG.Add(1)
		go func(id int) {
			defer writerWG.Done()
			for n := 0; n < 500; n++ {
				ri := (id + n) % ruleN
				ts := int64(n * 10) // 所有写者共用时间轴
				v := float64(100 - n%3)
				eng.Ingest("m", map[string]string{"k": string(rune('0' + ri))}, ts, v)
			}
		}(w)
	}
	// 评估者持续扫描，验证不变量：计数不减、状态合法。
	for r := 0; r < 2; r++ {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			prevFired := map[string]int{}
			for {
				select {
				case <-stop:
					return
				default:
				}
				sts, _ := eng.EvaluateAll(4990)
				for _, st := range sts {
					if st.FiredCount < prevFired[st.Rule.Name] {
						t.Errorf("FiredCount 倒退: %s", st.Rule.Name)
						return
					}
					prevFired[st.Rule.Name] = st.FiredCount
				}
			}
		}()
	}
	writerWG.Wait()
	close(stop)
	readerWG.Wait()

	// 最终稳定评估：所有规则数据均为高值且跨度足够，应处于 Firing。
	sts, _ := eng.EvaluateAll(4990)
	for _, st := range sts {
		if st.State != StateFiring {
			t.Fatalf("规则 %s 最终状态 %s，期望 firing", st.Rule.Name, st.State)
		}
		if st.FiredCount != 1 {
			t.Fatalf("规则 %s FiredCount=%d，期望恰好 1（重复/乱序未造成重复触发）",
				st.Rule.Name, st.FiredCount)
		}
	}
}
