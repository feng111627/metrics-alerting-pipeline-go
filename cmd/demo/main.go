// Command demo 在进程内演示指标采集、快照暴露与告警状态迁移。
//
// 运行: go run ./cmd/demo
package main

import (
	"fmt"
	"strings"

	"github.com/feng111627/metrics-alerting-pipeline-go/internal/alerting"
	"github.com/feng111627/metrics-alerting-pipeline-go/internal/metrics"
)

func main() {
	var now int64 = 1_000_000
	clock := func() int64 { return now }

	reg := metrics.NewRegistry(metrics.RegistryConfig{
		MaxSeriesPerMetric: 100,
		SeriesTTL:          15_000,
		Policy:             metrics.PolicyEvictOldest,
		NowMs:              clock,
	})

	requests := must(reg.RegisterCounter("http_requests_total", "HTTP 请求总数", []string{"path", "status"}))
	latency := must(reg.RegisterHistogramHandle("http_latency_seconds", "HTTP 延迟", []string{"path"},
		[]float64{0.05, 0.1, 0.25, 0.5, 1}))
	cpu := must(reg.RegisterGauge("cpu_usage", "CPU 使用率", []string{"host"}))

	eng := alerting.NewEngine(alerting.EngineConfig{NowMs: clock})
	if err := eng.AddRule(alerting.Rule{
		Name: "cpu_high", Metric: "cpu_usage",
		Labels: map[string]string{"host": "db-1"},
		Op:     alerting.OpGt, Threshold: 80,
		WindowMs: 10_000, ForMs: 3_000,
	}); err != nil {
		panic(err)
	}

	fmt.Println("== 模拟 6 个评估时刻 ==")
	cpuSeries := metrics.MustLabels(map[string]string{"host": "db-1"})
	for step, cpuVal := range []float64{50, 90, 91, 30, 95, 95} {
		now += 2_000

		// 指标写入（模拟并发采集的结果）。
		_ = requests.Inc(metrics.MustLabels(map[string]string{"path": "/api", "status": "200"}))
		_ = latency.Observe(metrics.MustLabels(map[string]string{"path": "/api"}), 0.12)
		_ = cpu.Set(cpuSeries, cpuVal)

		// 快照喂给告警引擎并评估。
		eng.IngestSnapshot(reg.Snapshot())
		st, ev, _ := eng.Evaluate("cpu_high", now)
		transition := ""
		if ev.To != ev.From {
			transition = fmt.Sprintf("  >>> 状态迁移 %s -> %s", ev.From, ev.To)
		}
		fmt.Printf("t%d  cpu=%4.0f  告警状态=%-9s%s\n", step+1, cpuVal, st.State, transition)
	}

	fmt.Println("\n== 最终一致快照 ==")
	snap := reg.Snapshot()
	for _, m := range snap.Metrics {
		fmt.Printf("指标 %s (%s), %d 条序列\n", m.Name, m.Type, len(m.Series))
		for _, s := range m.Series {
			pairs := make([]string, len(s.Labels))
			for i, p := range s.Labels {
				pairs[i] = fmt.Sprintf("%s=%s", p.Name, p.Value)
			}
			switch m.Type {
			case metrics.TypeHistogram:
				fmt.Printf("  {%s} count=%d sum=%.2f buckets=%v\n",
					strings.Join(pairs, ","), s.Histogram.Count, s.Histogram.Sum,
					s.Histogram.Buckets)
			default:
				fmt.Printf("  {%s} value=%v\n", strings.Join(pairs, ","), s.Value)
			}
		}
	}

}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
