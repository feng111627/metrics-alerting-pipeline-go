# 进程内指标采集与告警评估（Go）

一套纯内存、无外部依赖的进程内指标管线：注册/更新指标 → 取一致快照暴露 →
基于滑动时间窗评估告警。所有时间以可注入的毫秒时钟驱动，因此写入、快照与
状态迁移都是**确定性、可复现**的（测试里固定时钟，不依赖 wall time）。

```
internal/metrics    指标模型、注册表、基数控制、TTL、一致快照
internal/alerting   滑动窗口样本存储、告警规则与四态状态机
cmd/demo            端到端演示（固定时钟，输出状态迁移与最终快照）
```

## 一、指标模型

### 1.1 三类指标

| 类型 | 语义 | 更新方式 | 快照值 |
|------|------|----------|--------|
| Counter 计数器 | 单调不减 | `Inc` / `Add(delta>=0)` | `Value` |
| Gauge 仪表 | 可升可降 | `Set` / `Add` / `Inc` / `Dec` | `Value` |
| Histogram 直方图 | 累计分桶观测 | `Observe(v)` | 累计桶 + `Sum` + `Count` |

- **Counter 不允许负数增量**，返回可 `errors.Is` 判别的 `ErrNegativeValue`。
- **Histogram 桶**：注册时给有限的、（排序后）严格递增的上界；空输入使用
  默认 Prometheus 风格桶；内部自动补一个 `+Inf` 桶。非法桶返回
  `ErrInvalidBuckets`（NaN / -Inf / 重复边界）。桶计数是**累计**语义。

### 1.2 名称与标签

- 指标名：`[a-zA-Z_:][a-zA-Z0-9_:]*`；标签名：`[a-zA-Z_][a-zA-Z0-9_]*`。
  非法名称分别返回 `ErrInvalidName` / `ErrInvalidLabel`。
- 标签集通过 `metrics.NewLabels(map)` / `NewLabelsFromPairs(...)` 构造，
  内部**排序去重**并做 key 转义（`\`、`,`、`=`），保证不同标签集的
  内部 key 绝不冲突；pair 构造可检出重复键（`ErrDuplicateLabel`）。
- 更新某指标时，标签**键集合必须与注册时完全一致**，否则返回
  `ErrLabelMismatch`；键顺序无关（内部按排序后比较）。

### 1.3 基数上限与溢出策略（不允许无界增长）

`RegistryConfig.MaxSeriesPerMetric`（默认 1000）限制每个指标的标签组合数；
`MaxLabels`（默认 32）限制单条序列的标签数。达到上限后按策略处理：

- `PolicyReject`（默认）：拒绝创建新序列，返回 `ErrSeriesLimit`；
  已有序列照常更新，序列数保持有界。
- `PolicyEvictOldest`：淘汰 `lastUpdate` 最旧的序列，复用其槽位
  （槽位会先**整体重置**，新序列读不到旧值），再施加本次更新。

### 1.4 序列 TTL

`RegistryConfig.SeriesTTL > 0` 时，`lastUpdate <= now - TTL` 的序列：

- **不进入快照**；
- 在下次写入该指标时被**惰性回收**，因此过期槽位不占用基数名额；
- 单点读取视为不存在（值为 0）。

`Unregister(name)` 则删除整个指标及其全部序列，旧句柄再写返回
`ErrMetricNotFound`。

## 二、一致快照及其保证范围

`Registry.Snapshot() Snapshot` 返回某一时刻全部指标/序列的深拷贝：

```
Snapshot{ TakenAtMs, Metrics[] -> MetricSnapshot{ Name, Type, LabelNames,
                                                  Series[] -> SeriesSnapshot } }
```

**保证（在保证范围内成立）：**

1. **同一时刻**：快照只取一次读锁、只采样一次 `now`；快照里每条序列都
   来自同一逻辑时刻，直方图序列还带有 `LastUpdate <= TakenAtMs`。
2. **无半更新**：注册表用一把 `sync.RWMutex` 串行化所有值变更与快照读取；
   一次 `Inc/Set/Observe` 在锁内完成，快照要么看到它之前、要么之后，
   绝不会看到中间态（例如直方图 `Count` 与 `+Inf` 桶一定一致）。
3. **无脏数据**：已 `Unregister` 的指标、已过期（TTL）的序列不会出现。
4. **深拷贝隔离**：快照返回后，注册表继续写入不会改动旧快照。
5. 结果按指标名、序列 key 排序输出，**可复现、可逐字节比对**。

**范围之外（明确不保证）：**

- 快照是**采样时刻的一致**，不是跨时刻的事务视图；两次快照之间值会变化。
- 只保证单进程内、单个 `Registry` 实例的一致性；不涉及跨进程/持久化。
- 一致性针对**值结构**；浮点运算结果遵循 IEEE-754 本身的舍入规则。

并发安全验证见 `internal/metrics/snapshot_concurrent_test.go`（多写者 +
持续快照读者，断言计数/累计桶单调不减、序列完整、基数有界、无未来时间戳），
并在 `-race` 下运行。

## 三、告警规则与状态机

### 3.1 规则

```go
alerting.Rule{
  Name, Metric,                 // 规则名唯一；选一个指标
  Labels: {"host":"db-1"},      // 精确匹配选择器（允许序列带额外标签）
  Op: OpGt, Threshold: 80,      // >, >=, <, <=, ==, !=
  WindowMs: 10000,              // 滑动回看窗口
  ForMs:    3000,               // 条件连续成立多久才触发（<= Window，0=立即）
}
```

非法规则由 `Validate()` 拒绝（空名/空指标/非法操作符/窗口非正/For 为负/
For 超过窗口），错误为 `ErrInvalidRule`；重名规则返回 `ErrDuplicateRule`。

### 3.2 四个状态

```
                 首个(或最新)样本满足
        ┌──────────────────────────────┐
        │                              ▼
   ┌─────────┐  连续成立>=For   ┌─────────┐ 最新点违例 / 窗口内无新鲜点
   │ NoData  │ ───────────────▶ │ Firing  │ ────────────────┐
   └─────────┲━━━━━━━━━━━━━━━━▶ └─────────┘                  ▼
        ▲    └──── 不足For ───▶ ┌─────────┐            ┌────────────┐
        │                      │ Pending │ ◀──────────│ Recovered  │
        └──────────────────────┴─────────┘  下次评估  └────────────┘
             最新点违例/无数据(未触发过)                  (一次性通知)
```

- **NoData**：窗口内没有任何新鲜样本。
- **Pending**：有新鲜数据、末端存在连续匹配区间，但持续时长不足 `ForMs`。
- **Firing**：末端连续匹配区间的时间跨度 `newest - runStart >= ForMs`。
  触发后记住 `FiringSince`，窗口滑动期间保持触发；`FiredCount` 只在
  Pending→Firing 跃迁时 +1。
- **Recovered**：**一次性**通知态。Firing/Pending 期间一旦出现
  「最新点不满足」或「窗口内无新鲜点」，跃迁到 Recovered
  （`RecoveredCount` +1），**下一次评估自动回落**到 NoData/Pending。

### 3.3 窗口、过期与乱序/重复的处理

- 评估时先删除 `ts <= now - WindowMs` 的点，**过期点按无数据处理，
  绝不沿用旧值**继续触发/恢复判断。
- 样本以 `map[ts]value` 存储：
  - **重复到达**（相同 ts）只覆盖该点，不产生第二个点，不会把时长虚增、
    也不会重复触发；同 ts 的纠偏值以最后写入为准。
  - **乱序到达**按时间戳排序后计算「以最新点结尾的连续满足区间」，
    迟到的满足点可补齐持续时间而提前触发；迟到的违例点若成为最新点，
    立即恢复，不会漏报。
- `EvaluateAll(now)` 在同一逻辑时刻评估所有规则；`Event` 只在真正发生
  跃迁时产生（`To != From`），稳态重复评估不产生事件。
- `Engine.IngestSnapshot(metrics.Snapshot)` 用快照的单一时间戳，把其中
  counter/gauge 序列喂给匹配规则，实现「采集→暴露→告警」闭环。

### 3.4 验证方法

```bash
# 全部测试（普通模式）
go test ./...

# 并发测试必跑竞态检测
go test -race ./...

# 查看业务覆盖清单与每个用例名称/结果
go test ./... -v

# 端到端演示（固定时钟，输出状态迁移与最终一致快照）
go run ./cmd/demo
```

`go test -v` 启动时每个包先打印「业务覆盖清单」，随后每个顶层测试是一个
业务域、其下中文子测试是具体用例与结果（PASS/FAIL），例如：

- metrics：`TestRegistryRegistration`（注册校验）、`TestSeriesLimitReject`/
  `TestSeriesLimitEvictOldest`（基数与溢出）、`TestSeriesTTL`（过期）、
  `TestConcurrentWriteAndSnapshot`（并发写入+快照，-race）。
- alerting：`TestStateMachineFullLifecycle`（完整迁移）、
  `TestNoDataRecovery`（过期即无数据）、`TestDuplicateAndOutOfOrder`
  （重复/乱序）、`TestRegistrySnapshotIntegration`（快照联动）、
  `TestConcurrentIngestEvaluate`（并发下恰好触发一次）。

## 四、最小用法

```go
reg := metrics.NewRegistry(metrics.RegistryConfig{
    MaxSeriesPerMetric: 1000, SeriesTTL: 15000,
    Policy: metrics.PolicyEvictOldest,
})
c, _ := reg.RegisterCounter("http_requests_total", "", []string{"path"})
_ = c.Inc(metrics.MustLabels(map[string]string{"path": "/api"}))

snap := reg.Snapshot() // 任意时刻的一致快照

eng := alerting.NewEngine(alerting.EngineConfig{})
_ = eng.AddRule(alerting.Rule{Name: "hi", Metric: "m", Op: alerting.OpGt,
    Threshold: 80, WindowMs: 10000, ForMs: 3000})
eng.IngestSnapshot(snap)
status, event, _ := eng.Evaluate("hi", snap.TakenAtMs)
```
