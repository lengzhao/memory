# P0 修复说明（正确性 / 隔离 / 删除合规）

本轮修复针对架构评审里标记为 P0 的问题。全部改动都配有回归测试
（`service/fix_regression_test.go`，用例 `TestFix_*`），并在 `main` 分支上先复现、后修复。

## 1. 统一写入路径：决策引擎不再绕过服务层

`DecisionEngine.executeUpdate/executeMerge` 原先直接 `Updates()`，不重算 `tokenized_text`，
于是 `fts_memory` 一直停留在旧内容上——记忆按新内容**永远搜不到**。

现在 UPDATE / MERGE 都通过 `MemoryService.Update` 落库，因此同时获得：重新分词、`version`
自增、回调、审计事件。DELETE 通过 `MemoryService.Forget` 软删（可回滚），并清理索引。

- 回归：`TestFix_DecisionUpdateKeepsSearchIndexInSync`、`TestFix_SoftDeletePrunesSearchIndex`
- 行为变化：`ExecuteDecisions` 的 `Deleted`/`Updated` 返回目标 ID（原为原样透传 `TargetID`）。

## 2. 搜索索引与 status 联动

`fts_memory` 的成员定义明确为「status = active 的行」。新增 `AFTER UPDATE OF status` 触发器
处理 status 变迁（离开 active 即出索引，回到 active 即重新入索引），`CleanupExpired` 与
`Forget` 也会显式清理（触发器之外的双保险）。`Migrate` 结束时做一次
`INSERT/DELETE` 对账，修复历史库的孤儿行。

- 回归：`TestFix_SoftDeletePrunesSearchIndex`、`TestFix_CleanupExpiredPrunesIndex`
- 影响：`ValidateFTS` 在软删/过期后不再误报；`RebuildFTS` 回到"应急手段"定位。

## 3. 隔离改为 fail-closed

- 空白标识符**不再被静默替换为 `default`**（这是跨租户泄漏的入口）。
- 无隔离上下文 → `CodeUnauthorized`；身份不完整 → `CodeValidation`。
- 单用户/本地部署或维护任务用 `memory.AllowGlobal(ctx)` 显式放开全局命名空间。
- 隔离模式下显式传 `Namespace` 会被拒绝（原先 `Update` 完全不校验，可跨租户改数据）。
- `Update` 的作用域与版本 CAS 在同一条语句内生效。

- 回归：`TestFix_IsolationFailsClosed`、`TestFix_UpdateScopedAndVersioned`、`TestWithIsolation_BlankValuesFailClosed`、`TestAllowGlobal_MissingIsolationFailsClosed`

## 4. `Forget` 语义修正

原先未传 `NamespaceType` 会落到 **transient** 命名空间：按 ID 删除用户画像会**静默返回成功**
但什么都没删。现在：

- 隔离模式下 `NamespaceType` 为空 → 覆盖该调用方可读的**全部**命名空间；
- 返回真实影响行数；指定 `ItemIDs` 却零命中 → `CodeNotFound`。

- 回归：`TestFix_ForgetWithoutNamespaceTypeDeletesAcrossNamespaces`、`TestFix_ForgetUnknownIDReportsNotFound`

## 5. 删除合规（保留期落地）

`deleted_items.purge_after` 之前没有任何代码消费，等于 PII 永久明文留存。现在：

- `PurgeExpiredDeleted(now)` 按保留期清理归档副本；
- `PurgeDeletedNow(ids)` 立即清理指定条目（被遗忘权场景）；
- `Forget(mode=hard, BackupBeforeHardDelete=false)` 可直接物理删除且不留副本；
- `Janitor` 定时执行过期清理 + 归档清理（`NewJanitor(svc, cfg).Start(ctx)`，见下）。

- 回归：`TestFix_HardDeleteCanBePurged`、`TestFix_JanitorSweepsExpiryAndRetention`

## 6. 提取缓存：按隔离域分区 + 不再跳过落库

- `dialog_extractions` 新增 `scope_key`（tenant/user/session/agent），缓存查询按
  `(scope_key, dialog_hash, created_at)` 走新索引，**跨租户串味被消除**。
- **DryRun 不再写入 completed 缓存**（记为 pending），真实写入也不会命中缓存后直接
  `return` 而跳过落库；dry-run 探针不会污染后续真实写入。

- 回归：`TestFix_ExtractionCacheIsScoped`、`TestFix_DryRunDoesNotPoisonCache`

## 7. 入参校验

`importance` 必须 0-100、`confidence` 必须 0.0-1.0、`content` 非空且不超过
`MaxContentRunes`、`namespace_type` 必须是四个已知类型之一，越界即 `CodeValidation`
（原先 `Importance=9999` 会把排序彻底带偏）。

- 回归：`TestFix_ValidationRejectsOutOfRangeValues`

## 8. 召回排序

- FTS 候选改为 `ORDER BY bm25(...)`，不再按 rowid 截断；`FTSScore` 使用 BM25 转换值而非常量 `1.0`。
- 查询经 jiebago 分词后 OR 匹配，但命中文档必须包含**至少一个完整查询词**
  （分词会产生重叠 token，纯 OR 过松）。
- 最终分数归一化到 `[0,1]`（access boost 不再让分数越界）；同分按时间/ID 稳定排序。
- `MinConfidence` 可真正关闭（`SkipConfidenceFilter`）；此前 `0` 会被强制成 `0.5`，写入时未设
  confidence 的记忆会被默认过滤掉。
- LIKE 回退路径转义了 `%`/`_`。

- 回归：`TestFix_RecallConfidenceFilterAndRanking`、`TestFix_LongContentSearchable`

## 9. 策略层真正生效

- `GetPolicy` 支持**通配前缀规则**（`knowledge/projA/*`），此前通配策略被静默忽略。
- `inferNamespaceType` 支持隔离命名空间（`tenant/.../profile`）；此前一律落成 transient，
  导致隔离模式下 rank 权重、默认 TopK 全部错配。
- 新增 `Config.ApplyPolicies`：开启后 `Remember` 会按命名空间策略推导 `expires_at`
  （默认关闭以保持"不传 TTL 就永不过期"的既有行为）。

- 回归：`TestFix_PolicyPrefixMatchAndIsolatedInference`、`TestFix_TTLPolicyAppliedWhenEnabled`

## 10. 工程与可运维

- **连接池 + BUSY 重试**：`store.Config` 增加池大小与 busy_timeout；写路径（Create/Update）
  对 `SQLITE_BUSY` 做指数退避重试。
- **Janitor**：内建后台巡检（过期清理 + 归档保留期），宿主不再必须自己排定时任务。
- **可观测**：`Stats(ctx)` / `Health(ctx)` 暴露条目数、按类型分布、索引是否同步、
  归档积压等；`memory_events` 审计真正落库（`Config.RecordEvents` +
  `WithActor`/`WithTrace`）。
- **日志**：库内日志默认降到 WARN，不再往宿主输出刷 INFO。
- **LLM 超时**：`TimeoutSeconds` 未设时回退到 `DefaultLLMTimeout`（原先为 0 = 永不超时）。

- 回归：`TestFix_AuditEventsAndStats`、`TestFix_JanitorSweepsExpiryAndRetention`、`TestFix_JanitorStartStop`

## 兼容性说明（刻意的行为变更）

| 变更 | 旧行为 | 新行为 |
| --- | --- | --- |
| 无隔离上下文调用 | 读写共享 `*/default` 命名空间 | `CodeUnauthorized`，需 `AllowGlobal` |
| `WithIsolation` 传空串 | 替换为 `default` | 保留空值，身份不完整即报错 |
| `Forget` 未传类型 | 只作用于 transient，静默部分成功 | 覆盖可读全部命名空间，返回真实计数 |
| `Forget` 零命中 | 返回 `(0, nil)` | 指定 ID 时返回 `CodeNotFound` |
| `Forget(mode=hard)` | 一定留明文副本 | 默认留副本，可选不留；可用 `PurgeDeletedNow` 清理 |
| 提取缓存命中 + `DryRun=false` | 直接返回、不落库 | 正常落库 |
| `MinConfidence=0` | 强制 0.5 | 按 `DefaultMinConfidence`，可用 `SkipConfidenceFilter` 关闭 |
| 决策引擎 DELETE | 直接改 status、索引残留 | 走 `Forget`（软删 + 索引清理 + 审计） |

`executeUpdate` / `executeDelete` 现在是包内私有且返回 `(id, error)`，仅测试引用；公开 API 未变。