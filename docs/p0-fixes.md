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

## 6. 上下文装配：召回结果按预算裁剪

`Recall` 只按 TopK 限条数，长记忆会把 prompt 预算吃光。新增 `RecallContext`：

```go
bundle, err := svc.RecallContext(ctx, scope, memory.RecallRequest{
    NamespaceTypes: []memory.NamespaceType{memory.NamespaceTransient, memory.NamespaceAction},
}, memory.RecallBudget{
    MaxTokens:               800,
    MaxItems:                10,
    PerItemMaxTokens:        120,
    IncludeSummaryText:      true,   // 有 summary 时用 summary 而不是全文
    IncludeNamespaceSummary: true,   // 追加命名空间概览
    NamespaceSummaryMaxTokens: 200,
    Formalizer:              nil,    // 可选：自定义渲染
})
// bundle.Text / bundle.Tokens / bundle.Items / bundle.NamespaceSummaries / bundle.Truncated
```

设计取舍：

- **预算按渲染后的文本计量**（含 bullet、分隔符、`[truncated]` 标记），不是按裸记忆文本：
  "什么会被模型读到"才是要放得下的东西，`bundle.Tokens` 因此等于 `CountTokens(bundle.Text)`。
- **裁剪可解释**：`Items[].Truncated` 标出被截断的条目，`Bundle.Truncated` 标出有丢件；
  被截断的条目全文仍在库里，可按 ID 取回。
- **`PerItemMaxTokens` 与 `MaxTokens` 是两级预算**：前者防止单条长记忆独占，后者是总量上限。
- **计数可替换**：默认 `DefaultTokenCounter` 是无依赖估算（CJK 每字 1 token，拉丁约 4 字符 1 token，
  向上取整）。宿主用 `memory.SetTokenCounter(tiktoken 包装)` 换成精确计数即可。
- **摘要不是 LLM 调用**：命名空间概览是确定性的（条数 + 最重要条目的首句 + 标签热度），
  在召回路径上不引入网络调用与延迟。

## 7. 命名空间摘要与条目摘要

`NamespaceSummary` 之前是个空壳（`SummaryEnabled`/`SummaryItemTokenThreshold` 无人消费，
`SummaryGenerator` 只做截断）。现在：

- `scope.Policy(nsType)` 给出绑定到该 scope 命名空间的基线策略，`SetPolicy` 全字段落库（含 `false`/`0`）；
- `RefreshNamespaceSummary` 生成并**缓存**命名空间概览，缓存键是内容指纹
  （活跃条数 + 各条 ID + 更新时间），内容变了或超过 10 分钟才重算（`Stale` 标明是否重算）；
- `GenerateNamespaceSummary` 是显式重算版本；
- 摘要只在 `SummaryEnabled` 为真时生成，且受 `SummaryItemTokenThreshold` 限制每条贡献的篇幅；
- `SummaryGenerator.GenerateItemSummary(ctx, scope, id, maxTokens)` 按句边界派生条目摘要
  （标题占 1/4 预算，其余给正文首句），不调用 LLM；
- 越权访问返回 `UNAUTHORIZED`（命名空间不属于该 scope），不泄露存在性。

**注意 GORM 零值陷阱**：`NamespacePolicy` 的列不再声明数据库默认值。GORM 会跳过"声明了默认值且为零值"
的字段，导致 `SummaryEnabled=false` 被静默写成 `true`。现在字段默认值由
`DefaultNamespacePolicy` / `Scope.Policy` 在 Go 侧提供，写入是全字段的，`false` 能真正落库。

## 8. 检索：把"能查到"和"排得准"分开

### 8.1 自然语言查询直接可用

查询与索引走同一套分词（jiebago search 模式，会同时产出最长词与嵌入的短词），所以前端可以
直接传一句正常的话，不需要先做关键词抽取：

| 查询 | 结果 |
| --- | --- |
| `SQLite 并发读` | 命中（fts） |
| `数据库 并发 写 超时 参数` | 命中（fts） |
| `并发写为什么失败` | 命中（fts） |
| `busy_timeout` | 命中（fts） |

索引不再做 head+tail 折叠（原来只索引头 100 + 尾 100 字符），改为整体索引、仅对超长文本
（> 4000 字）截断，因此关键词在正文任何位置都查得到。

**没有查询改写、没有向量检索**：库内不做语义转换。像 `上次说的那个超时问题是什么` 这种不含任何
共有实词的查询会返回 0 条——前端负责把它转成正常语义的表达（用户已确认由前端承担）。

### 8.2 BM25 之前根本没在工作（真 bug）

SQLite 的 `bm25()` 返回**负值，越负越相关**。原实现写的是：

```go
if bm25 <= 0 { return 1.0 }   // 结果：所有 FTS 命中都变成 1.0，排序退化为按重要度
```

于是相关性排序完全失效——这正是原代码里 `FTSScore = 1.0` 硬编码能"看起来正常"的原因。
现在映射为 `relevance = raw / (raw + 1)`（`raw = -bm25`），绝对尺度、跨查询可比：
强匹配 ≈ 0.67、仅共享一个 token ≈ 0.33。

e2e 测试里"结果必须按 importance 降序"的断言其实是这个 bug 的副产品，已改为断言真实契约
（分数非递增、且每个命中都带非零文本相关度）。

### 8.3 子串扫描是显式降级，不是混在 BM25 里

只有分词后没有任何索引词（例如查询就是 `★` 这类符号）才会走子串扫描，此时
`MatchReasons` 报 `substring_match`、`FTSScore` 取固定值 `0.5`；索引命中报 `fts_match`。
两种来源不在同一尺度上，因此不为它们假装算平均。
