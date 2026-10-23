# 待办

P0（正确性 / 隔离 / 删除合规）已在 `dev` 分支完成，实现约定见 README「隔离模型」与
`docs/p0-fixes.md`。以下为后续路线。

## 已完成（架构基线）

- **显式隔离**：`Scope` 是一等参数（`NewScope`/`MustScope`/`GlobalScope`），命名空间由身份推导，
  所有读写都带 scope；跨租户的 recall/list/update/touch/forget 不可达。
- **唯一写入路径**：抽取器与决策引擎落库都经 `MemoryService`（索引、版本、回调、审计不外绕）。
- **索引不变式**：`fts_memory` 恒等于 `status = active` 的行；状态变迁由触发器 + `pruneFTS` 双向维护；
  `Migrate` 收尾对账；`ValidateFTS` 可信。
- **删除**：`soft`/`expire`/`hard`；硬删默认留可恢复副本、保留期由 `Janitor` 执行；
  `BackupBeforeHardDelete=false` 与 `PurgeDeletedNow` 提供立即擦除。
- **可用性**：BM25 排序召回 + 完整词过滤 + 归一化打分；`DedupeKey` 按命名空间唯一域分区且并发幂等；
  入参/枚举校验；策略支持通配前缀并驱动权重与 TTL。
- **可运维**：`Stats`/`Health`/`Janitor`、连接池与 `SQLITE_BUSY` 退避、`memory_events` 审计、
  库日志默认 WARN、LLM 超时兜底。

## P1 — 记忆质量（下一步）

- [ ] **Token 预算装配**：`Recall` 现在只按条数裁剪。需要 `RecallBudget{MaxTokens, ReserveForPrompt}`、
      会话滚动摘要、`NamespaceSummary` 的真正维护（`SummaryEnabled` / `SummaryItemTokenThreshold` /
      `DefaultTopK` 目前仍无引擎消费，`SummaryGenerator` 是截断占位实现）。
- [ ] **语义召回**：`EmbeddingRef` 是死字段。需要 embedding 接口 + 向量索引（sqlite-vec 或外部索引）+
      与 BM25 的混合排序 + 可选 rerank，用于同义改写与口语化查询。
- [ ] **时序有效性**：`valid_from/valid_to`、as-of 查询、`supersedes` 链。矛盾检测目前只有
      `memory_links` 表结构、无写入方。
- [ ] **字段级合并且可解释**：MERGE 目前是 LLM 给文本或拼接 `[Additional Info]`。需要结构化合并策略
      （数值取最新、列表去重、时间取最近）与冲突原因留痕。
- [ ] **LLM provider 抽象**：接口化（Anthropic/Gemini/本地）、重试/退避/限流、成本与 token 计量、
      输出 schema 校验、`httptest` 测试替身（当前提取/决策链路无法离线测试）。
- [ ] **写入确认与反馈**：propose→确认队列，按用户反馈强化/遗忘（当前只有被动 `access_count`）。
- [ ] **footprint 控制**：jiebago 内置大词典无 build tag 开关，边缘 Agent 需要可选裁剪。

## P2 — 规模化与运维

- [ ] **schema 版本化**：目前只有 AutoMigrate + 一次性 `DROP INDEX` 特判；需要 version 表与前滚/回滚脚本。
- [ ] **导出/导入/备份**：JSONL / Markdown 导出、快照恢复、`VACUUM` 与 FTS `optimize` 调度。
- [ ] **多进程写入**：当前依赖 `busy_timeout` + 退避重试与单连接池；需要单写者队列或按 tenant 分库。
- [ ] **构造期配置**：`Config` 经可变 `WithConfig` 设置（须在服务被共享前调用），应改为构造期 options。
- [ ] **`service/*.go` 继续拆分**：`forget.go`（534 行）仍同时承载 CAS 更新、删除与索引维护，可再分为 `update.go` / `purge.go`。
- [ ] **提取质量评测集**：固定对话 → 期望记忆的回归集，配合 prompt 版本化做离线评估。

## 已知遗留

- `test/concurrent_test.go` 的 4 个用例仍是 `t.Skip`（已迁移到 `concurrent_sqlite_test.go`）。
- 决策引擎对"目标不在候选命名空间内"的决策记为 error 并跳过（不再默认 ADD），宿主需监控 `Errors`。
- `examples/04`、`05`、`06` 尚未 gofmt（历史遗留），拆分时一并处理。
