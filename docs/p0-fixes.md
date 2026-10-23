# 设计说明：隔离、索引与删除（v0.4）

这不是"补丁版"的说明文档，而是当前核心设计的契约说明。三处关键取舍如下。

## 1. 隔离身份是显式参数

```go
scope, err := memory.NewScope(tenantID, userID, sessionID, agentID)  // 四项必填
svc.Remember(ctx, scope, memory.RememberRequest{...})
svc.Recall(ctx, scope, memory.RecallRequest{...})
```

**为什么不放在 context 里。** 身份藏在 context 里时，"忘了传"和"传了默认值"在类型上无法区分，
这两种情况在多租户下都会变成跨用户读写。改成显式参数后：

- 忘记传 → **编译错误**；
- 传了不完整身份 → `VALIDATION`（不再静默替换成 `default`）；
- 单用户/本地/维护任务 → `memory.GlobalScope()`，语义清晰且是主动选择。

命名空间布局：

| 类型 | 命名空间 | 可见范围 |
| --- | --- | --- |
| profile / action / knowledge | `tenant/<t>/user/<u>/<type>` | 同一用户跨会话共享 |
| transient | `tenant/<t>/user/<u>/session/<s>/agent/<a>/transient` | 仅单会话单 agent |

**按 ID 的写操作也要过 scope。** ID 本身不携带归属信息，所以 `Update`/`Touch`/`RenewExpiration`/
`Forget` 都会带上 scope 的命名空间集合作为谓词，`Update` 更是把归属判断和版本 CAS 放在同一条语句里：
越权的 ID 表现为 `NOT_FOUND`，既不会改数据，也不泄露存在性。决策引擎额外用候选的 `namespace_type`
约束目标，模型幻觉出的 ID 无法越出它被分类到的命名空间。

## 2. 写入路径唯一

`MemoryService` 是唯一写出口。抽取器（`persistMemory`）和决策引擎（ADD/UPDATE/UPDATE/MERGE/DELETE）
都调用它，因此下列不变量不会因为某条路径"绕过去"而失效：

- `tokenized_text` 与内容同步（改完内容仍能按新内容搜到）；
- `version` 单调递增（CAS 冲突可被发现）；
- 生命周期回调与 `memory_events` 审计不丢；
- 删除会同时清索引。

幂等也收敛到一处：抽取落库用内容派生的 `DedupeKey`，而不是"先查后写"——后者在并发下会双写。

**DedupeKey 的唯一域按命名空间定义：** transient 是会话级，profile/action/knowledge 是用户级。
把会话塞进所有 key 的前缀（历史实现）会让"跨会话同一个事实"永远去不掉重。

## 3. 搜索索引是不变量

**契约**：`fts_memory` 恰好持有 `status = 'active'` 的行，一一对应。

- INSERT 且 active → 入索引（触发器）；
- UPDATE 改内容 → 刷新索引（触发器）；
- status 进出 active → 入/出索引（双向触发器）；
- 服务层在改状态的同一流程里显式 `pruneFTS`（触发器是兜底，不是唯一防线）；
- `Migrate` 收尾做一次 INSERT/DELETE 对账，修复历史库；
- `DELETE` → 出索引（触发器）。

因此 `ValidateFTS` 的语义变可信（不再因软删而误报），`RebuildFTS` 回到"应急修复"定位，
`Stats.IndexInSync` 可以直接作为告警指标。

## 4. 删除与保留期

| 模式 | 效果 | 可恢复 |
| --- | --- | --- |
| `soft` | 标记删除，出索引，读操作不可见 | 是（改回 status） |
| `expire` | 标记过期，出索引 | 是 |
| `hard` | 物理删除（默认留副本） | 取决于 `BackupBeforeHardDelete` |

- `Forget` 未指定 `NamespaceType` → 覆盖该 scope 可读的**全部**命名空间（"忘记这个用户"的语义）。
  历史实现会静默落到 transient，返回成功却没删到画像数据。
- 指定 ID 且零命中 → `CodeNotFound`，不返回"成功删了 0 条"。
- 保留期由 `Janitor` 执行：`PurgeExpiredDeleted(now)`；立即擦除用 `PurgeDeletedNow` 或
  `BackupBeforeHardDelete=false`。
- 模型提议的 DELETE 一律走 soft：一次错误决策不应不可恢复。

## 5. 其它刻意的取舍

- **召回打分**：文本相关度用 BM25 转换值（不再是常量 1.0），候选集 `ORDER BY bm25`，
  归一化到 `[0,1]`；命中必须包含完整查询词（中文分词会产生重叠 token，纯 OR 过松）。
- **confidence 过滤**：未设置时用 `DefaultMinConfidence`（0.5），`SkipConfidenceFilter` 可完全关闭；
  写入未评分的记忆（列默认 1.0）不会被默认过滤掉。
- **LIKE 兜底**：仅当分词索引无法服务该查询时启用，且转义 `%`/`_`，用户输入不会被当通配符。
- **策略**：`GetPolicy` 支持通配前缀（`knowledge/projA/*`），按最长前缀取胜；隔离命名空间能正确
  推断类型（否则会拿到 transient 的 TTL 与权重）。`Config.ApplyPolicies` 开启后按策略推导过期时间。
- **运维**：连接池收敛为单写者 + `busy_timeout`，写路径对 `SQLITE_BUSY` 指数退避重试；
  库内日志默认 WARN，不污染宿主输出；LLM 调用有默认超时兜底。
