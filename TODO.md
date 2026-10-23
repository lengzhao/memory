# 待办（架构评审落地）

状态基于 `dev` 分支。P0 已在 `750a8c2` 完成，P1/P2 为后续计划。

## P0 — 正确性 / 隔离 / 合规（已完成）

- [x] **统一写入路径**：决策引擎 `executeUpdate/executeMerge` 改为走 `MemoryService`（重算
      tokenized_text、版本 CAS、回调、审计）；`executeDelete` 改为走 `Forget` 软删 + 索引清理。
- [x] **FTS 与 status 联动**：新增 `AFTER UPDATE OF status` 触发器（进出 active 双向同步）、
      `Migrate` 收尾对账、`Forget`/`CleanupExpired` 显式清理；`ValidateFTS` 不再误报。
- [x] **隔离 fail-closed**：空白标识符不再替换为 `default`；无身份 → `UNAUTHORIZED`；身份不完整
      → `VALIDATION`；新增 `AllowGlobal(ctx)`；隔离模式下显式 `Namespace` 被拒。
- [x] **`Update` 作用域**：命名空间作用域与版本 CAS 合并为单条语句，杜绝跨租户更新。
- [x] **`Forget` 语义**：未传 `NamespaceType` 覆盖可读全部命名空间；返回真实行数；零命中报
      `NOT_FOUND`。
- [x] **删除合规**：`PurgeExpiredDeleted` / `PurgeDeletedNow` / `BackupBeforeHardDelete`；
      硬删除不再必然留下永久明文副本。
- [x] **提取缓存分区**：新增 `scope_key` + `(scope_key, dialog_hash, created_at)` 索引；DryRun 不入
      completed 缓存；缓存命中不再跳过落库。
- [x] **入参校验**：importance 0-100、confidence 0-1、content 非空且 ≤ `MaxContentRunes`、
      namespace_type 白名单。
- [x] **召回排序**：BM25 排序候选 + 完整词过滤 + 分数归一化 + 可关闭的 confidence 过滤 +
      LIKE 转义回退。
- [x] **策略生效**：通配前缀匹配、隔离命名空间类型推断、`Config.ApplyPolicies` 时按策略推导 TTL。

## P1 — 可用性（未开始）

- [ ] **应用提取时的话题合并策略**：MERGE 目前是"拼接 + [Additional Info]"，需要可配置的字段级合并
      （数值取最新、列表去重、时间取最近），并记录冲突原因。
- [ ] **Token 预算装配**：`Recall` 目前只按条数裁剪；需要 `RecallBudget{MaxTokens, Reserve}`、会话滚动
      摘要、`NamespaceSummary` 的真正维护（当前 `SummaryGenerator` 是截断占位实现，
      `SummaryEnabled`/`SummaryItemTokenThreshold`/`DefaultTopK` 仍无引擎消费）。
- [ ] **语义召回**：`EmbeddingRef` 仍是死字段；需要 embedding 接口 + 向量索引（或 sqlite-vec）+ 与 BM25
      的混合排序 + 可选 rerank。
- [ ] **LLM 抽象层**：provider 接口（Anthropic/Gemini/本地）、重试/退避/限流、成本与 token 计量、
      schema 校验（当前只靠 `json_object`）、`httptest` 测试替身（当前全库无 mock）。
- [ ] **时序有效性**：`valid_from/valid_to`、as-of 查询、`supersedes` 链；矛盾检测目前只有
      `MemoryLink` 表结构、无写入方。
- [ ] **写入策略/确认队列**：propose→确认、按反馈强化/遗忘（当前只有被动 `access_count`）。
- [ ] **footprint 控制**：jiebago 内置大词典无 build tag 开关，边缘 Agent 需要可选裁剪。

## P2 — 运维与规模化（未开始）

- [ ] **schema 版本化**：目前只有 AutoMigrate + 一次性 `DROP INDEX` 特判；需要 version 表与前滚/回滚脚本。
- [ ] **导出/导入/备份**：JSONL/Markdown 导出、快照恢复、`VACUUM` / FTS `optimize` 调度。
- [ ] **多进程写入**：单写者队列（当前靠 busy_timeout + 重试），按 tenant 分库/分片。
- [ ] **API 演进**：构造期 options 替代可变 `WithConfig`、`RememberMany/RecallMany`、游标分页、
      去掉根包 `var NewX =` 别名以恢复 Go doc。
- [ ] **`service/memory.go` 拆分**：单文件 1200+ 行，按 remember/recall/forget/maintenance 切分
      （`Recall` 的 codegraph 爆炸半径为 30 个符号）。
- [ ] **提取质量评测集**：建立固定对话 → 期望记忆的回归集，配合 prompt 版本化做离线评估。

## 已知遗留

- `test/concurrent_test.go` 的 4 个用例仍为 `t.Skip`（已迁移到 `concurrent_sqlite_test.go`）。
- `service/memory.go` 中 `min` 为自定义实现（Go 1.21+ 有内建 `min`），拆分文件时一并清理。
- 决策引擎仍把"解析失败的 decision"默认当作 `ADD`；后续应改为显式忽略 + 告警。