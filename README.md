# metrics-alerting-pipeline-go

进程内的指标采集与告警评估组件。用内存时间序列模拟真实的写入与查询路径，
保证并发写入、快照暴露和告警状态迁移都稳定且可复现（测试使用手动时钟与
race detector 验证）。

## 指标模型（`metrics` 包）

支持三类指标，均按「指标族（Vec）+ 标签组合 → 序列」组织：

| 类型 | 更新操作 | 约束 |
| --- | --- | --- |
| Counter | `Inc(labels, delta)` | `delta` 必须为有限值且 ≥ 0 |
| Gauge | `Set(labels, v)` / `Add(labels, delta)` | 值不得为 NaN，增量必须有限 |
| Histogram | `Observe(labels, v)` | 注册时桶边界必须有限、非空、严格递增；观测值不得为 NaN |

### 注册与失败原因

所有失败原因都是可 `errors.Is` 判定的哨兵错误（`metrics/errors.go`）：

- `ErrInvalidName`：指标名为空、超长或不符合 `[a-zA-Z_:][a-zA-Z0-9_:]*`；
- `ErrInvalidLabel`：标签名非法、值过长或标签数超限（每序列 ≤ 16 个）；
- `ErrDuplicateRegistration`：同名指标族重复注册（无论类型是否相同）；
- `ErrConstraintViolation`：违反类型约束（负计数器增量、空帮助文本、非法桶边界等）；
- `ErrCardinalityLimit`：序列总数达到上限；
- `ErrUnknownMetric`：查询不存在的指标。

值的校验发生在序列创建**之前**，因此失败的写入不会留下空序列。

### 标签基数上限

`NewRegistry(WithMaxSeries(n))` 设置全注册表序列总数上限（默认 10000）。
达到上限后的既定策略：**拒绝创建新标签组合的序列**（返回
`ErrCardinalityLimit`），已有序列的更新不受影响；`Delete` 删除序列后名额
立即释放。序列数不会无界增长。

## 快照一致性（`Registry.Snapshot`）

并发模型：所有写入（Inc/Set/Add/Observe/Delete/注册）持有注册表读锁，
`Snapshot` 持有写锁。因此快照与任何写入互斥：

- 快照中的每条序列都来自**同一逻辑时刻**，等价于某一瞬间的完整状态；
- 绝不会读到半更新的值（例如直方图的桶计数与 Sum 分属两次写入的中间态）；
- 已 `Delete` 的序列不会出现在快照中；
- 快照内容是深拷贝，调用方修改不影响注册表。

保证范围：一致性仅覆盖「同一时刻所有序列的值」这一快照语义；快照不阻塞
后续写入，拿到快照后注册表可继续变化。

## 告警规则与状态机（`alerting` 包）

每条 `Rule` 定义：指标名、滑动窗口 `Window`、待触发时长 `PendingFor`、
过期时间 `StaleAfter`、比较运算（`Gt/Ge/Lt/Le`）与阈值。

### 评估语义（在时刻 `now`）

1. 取窗口 `(now-Window, now]` 内时间戳最新的样本作为有效值；
2. 窗口内无样本，或最新样本距今超过 `StaleAfter` → **NoData**（不沿用旧值）；
3. 有效值满足阈值条件且已连续满足满 `PendingFor` → **Firing**；
4. 满足但时长不足 → **Pending**（条件中断后重新计时）；
5. 有数据但条件不满足 → **Resolved**（未触发/已恢复）。

状态迁移以 `Transition` 结构返回，只有状态真正变化时才产生迁移记录，
不会重复触发。

### 去重与乱序

- 相同指标相同时间戳的样本只接受第一个（`Ingest` 返回 `false`）；
- 乱序样本按时间戳插入有序缓冲，评估永远基于完整缓冲重算，
  因此乱序到达不会造成重复触发或漏报恢复。

### 可复现性

`Evaluator` 的评估时刻由调用方显式传入（`Evaluate(now)`），
也可用 `FakeClock` 驱动 `EvaluateNow`，测试中时间完全可控。

## 验证方法

```sh
go test ./... -race -v
```

测试日志按业务分组，覆盖：

- 指标注册：重复注册 / 非法名称 / 非法直方图桶的可区分错误；
- 指标更新：非法标签、负计数器增量、三类指标的值与快照核对；
- 标签基数：上限拒绝新序列、已有序列可更新、删除释放名额、并发创建不超上限；
- 并发写入与快照：8 写者 x 500 轮写入期间持续取快照，校验
  「先写 c_a 后写 c_b ⇒ 快照中 c_a ≥ c_b」与直方图「末桶累计 == Count」
  两条不变量（race detector 下运行）；
- 告警状态机：NoData→Pending→Firing→Resolved 全链路、过期按无数据判定、
  重复/乱序样本、窗口滑动、Pending 中断重计时。
