# memory

一个简单的基于 SQLite 的记忆系统，用于给 AI Agent 内嵌使用。

## 技术栈

- **Go** + **GORM**
- **SQLite** (FTS5 全文搜索)
- 本地优先、单用户、低运维

## 快速开始

### 基础功能（无需API Key）

```bash
# 运行基础功能示例（会自动初始化数据库）
go run examples/01_basic/main.go
```

### LLM自动提取（使用OpenAI API）

```bash
# 1. 设置环境变量
export OPENAI_API_KEY=sk-your-api-key

# 2. 运行完整 LLM 提取演示
go run examples/08_extract_demo/main.go
```

**详细配置说明**：[docs/openai-setup.md](docs/openai-setup.md)

### 环境变量配置

```bash
# 必需
export OPENAI_API_KEY=sk-your-api-key

# 可选（有默认值）
export OPENAI_MODEL=gpt-5.4-nano  # 默认模型 (gpt-5.4-nano/gpt-4o/gpt-4)
export OPENAI_BASE_URL=            # 自定义API端点（如 Azure OpenAI）
export OPENAI_TEMPERATURE=0.3      # 温度参数（0-1，越低越稳定）
export OPENAI_TIMEOUT=30           # API超时时间（秒）
```

### 使用本地模型（Ollama，免费）

```bash
# 1. 安装并启动 Ollama
ollama pull llama3.1:8b

# 2. 配置环境
export OPENAI_BASE_URL=http://localhost:11434/v1
export OPENAI_MODEL=llama3.1:8b
export OPENAI_API_KEY=ollama

# 3. 运行
go run examples/08_extract_demo/main.go
```

## 自动提取演示

```bash
$ go run examples/08_extract_demo/main.go

============================================================
LLM Memory Extraction Demo
============================================================

--- Dialog 1 ---
Input: 我喜欢用深色主题，浅色主题太刺眼了
Status: completed (Processing time: 0ms)

  Memory 1:
    Namespace: profile     👤 用户画像
    Title: User Preference
    Confidence: 0.85

--- Dialog 2 ---
Input: 今天的任务是完成用户登录功能的重构，优先级高
Status: completed (Processing time: 0ms)

  Memory 1:
    Namespace: action      📋 任务行动
    Title: Task Item
    Confidence: 0.90

--- Dialog 3 ---
Input: Go语言的goroutine是轻量级线程，由Go运行时管理
Status: completed (Processing time: 1ms)

  Memory 1:
    Namespace: knowledge   📚 知识
    Title: Knowledge Item
    Confidence: 0.95

Total memories: 4
- [profile] User Preference (conf: 0.85)
- [action] Task Item (conf: 0.90)
- [knowledge] Knowledge Item (conf: 0.95)
```

## 项目结构

```
memory/
├── examples/            # 可执行示例（Scope 接入、幂等写入、TTL、策略、LLM 提取）
│   ├── 01_basic/        # remember/recall/list/update/forget 全流程
│   ├── 02_dedupe/       # DedupeKey 幂等
│   ├── 03_ttl/          # TTL / 滑动续期 / CleanupExpired
│   ├── 04_callback/     # 生命周期回调
│   ├── 05_extract/      # LLM 提取
│   ├── 06_policy/       # 命名空间策略（含通配前缀）
│   └── 08_extract_demo/ # 多轮对话 LLM 提取落库
├── model/               # GORM 模型（表结构单一来源）
│   └── memory.go
├── service/             # 业务逻辑，按职责拆分
│   ├── scope.go         # 隔离身份与命名空间布局（Scope）
│   ├── remember.go      # 写入（幂等、校验、TTL/策略）
│   ├── recall.go        # 召回 / 列表 / 打分 / 检索
│   ├── forget.go        # 删除、保留期、索引维护、CAS 更新
│   ├── extractor.go     # LLM 对话→记忆提取（按 scope 分区缓存）
│   ├── decision.go      # ADD/UPDATE/DELETE/IGNORE/MERGE 决策
│   ├── policy.go        # 命名空间策略（精确 + 通配前缀）
│   ├── janitor.go       # 后台巡检（过期 + 归档保留期）
│   ├── health.go        # Stats / Health
│   ├── events.go        # 审计事件
│   ├── retry.go         # SQLITE_BUSY 退避重试
│   └── tokenizer.go     # jiebago 中文分词
├── store/               # DB 连接与 schema：`Migrate` = AutoMigrate + FTS5
│   ├── db.go            # DSN/pragma、连接池
│   └── fts.go           # FTS5 虚表与状态同步触发器
├── docs/
│   ├── memory.md        # 详细技术方案
│   └── p0-fixes.md      # 隔离/索引/删除的设计取舍
├── TODO.md              # 后续路线（语义召回、摘要、provider 抽象等）
├── go.mod
└── README.md
```

**Schema**：仅通过 `memory.Migrate(db)`（或 `store.Migrate`）初始化。GORM `AutoMigrate` 创建业务表；`store` 内建 FTS5 虚表与同步触发器。提取 **Prompt 可不建表行**：未配置 `is_default` 的 `extraction_prompts` 时，`Extract` 使用 `service` 包内建默认（`dialog_extractions.prompt_id` 记为 `prompt-default-v3`（Run Journal 证据可信度；**profile** 仅面向终端用户，不由宿主再做短语过滤）。需要定制时在 DB 中新增/设置 `is_default` 即可。不使用独立 SQL 迁移目录。

> **集成说明**：`import "github.com/lengzhao/memory"` 即可；也可按需引用子包 `model`、`service`、`store`。

## 核心特性

- ✅ 多 namespace 分层存储 (transient/profile/action/knowledge)
- ✅ **并发控制**：乐观锁 (`version` 字段)
- ✅ **幂等写入**：`dedupe_key`（同一 namespace 内唯一）等
- ✅ **TTL 策略**：fixed / sliding / manual 三种模式
- ✅ **可恢复删除**：软删后保留在 `deleted_items` 表
- ✅ **全文搜索**：FTS5 虚表 + 自动同步触发器
- ✅ **策略持久化**：`namespace_policies` 表存储配置
- ✅ **事件追踪**：审计日志 (`memory_events`)
- ✅ **LLM 集成**：OpenAI 兼容接口（可通过 `OPENAI_BASE_URL` 对接兼容端点，如 Ollama）
- ✅ **自动提取**：对话内容分类到上述 4 类 namespace

## 数据模型

### 主要表

| 表名 | 说明 |
|------|------|
| `memory_items` | 核心记忆条目 |
| `memory_links` | 记忆间关系 |
| `namespace_summaries` | 命名空间摘要 |
| `namespace_policies` | 命名空间策略配置 |
| `memory_events` | 审计事件日志 |
| `deleted_items` | 软删恢复表 |
| `memory_merges` | 合并操作记录（决策引擎） |
| `fts_memory` | FTS5 全文搜索虚表（由 `Migrate` 创建） |
| `dialog_extractions` | 对话提取记录（幂等检测） |

> 说明：`LLMConfig` 和 `ExtractionPrompt` 当前为运行时配置结构体（代码传入），不作为持久化表自动创建。

详见 `model/memory.go` 与 `docs/memory.md`。

## 隔离模型（v0.4）

隔离身份是一个**显式参数**，不是 context 里的隐式值：

```go
scope := memory.MustScope(tenantID, userID, sessionID, agentID)   // tenant/user/session/agent 四项必填
svc.Remember(ctx, scope, memory.RememberRequest{...})
svc.Recall(ctx, scope, memory.RecallRequest{...})
```

忘记传身份是编译错误，而不是运行时的跨租户读写。命名空间布局：

| 类型 | 命名空间 | 可见范围 |
| --- | --- | --- |
| `profile` / `action` / `knowledge` | `tenant/<t>/user/<u>/<type>` | 同一用户跨会话共享 |
| `transient` | `tenant/<t>/user/<u>/session/<s>/agent/<a>/transient` | 单会话单 agent |

单用户 / 本地 / 维护任务用 `memory.GlobalScope()`（数据落在 `profile/default` 等全局命名空间）。

**写入路径唯一**：抽取器与决策引擎的落库都经过 `MemoryService`，索引、版本号、回调、审计不会被绕过。

**删除**：`soft`（可回滚，出索引）、`expire`、`hard`（默认留一份可恢复副本，保留期 `DefaultDeletedRetention`）。
真正的擦除路径是 `Forget(mode=hard, BackupBeforeHardDelete=false)` 或 `PurgeDeletedNow`。
"忘记这个用户"直接 `Forget(ctx, scope, memory.ForgetRequest{})` 覆盖该 scope 可读的全部命名空间。

```go
// 后台巡检（过期清理 + 归档保留期）
janitor := memory.NewJanitor(svc, memory.JanitorConfig{Interval: time.Hour})
janitor.Start(ctx); defer janitor.Stop()

// 健康检查 / 运营指标
stats, _ := svc.Stats(ctx)   // 条目分布、索引是否同步、归档积压、抽取失败数
err := svc.Health(ctx)
```

### 上下文装配（按预算召回）

```go
bundle, err := svc.RecallContext(ctx, scope, memory.RecallRequest{
    NamespaceTypes: []memory.NamespaceType{memory.NamespaceTransient, memory.NamespaceAction},
}, memory.RecallBudget{
    MaxTokens:                 800,   // 记忆块总预算（按渲染后文本计量）
    MaxItems:                  10,
    PerItemMaxTokens:          120,   // 单条上限，防止一条长记忆独占
    IncludeSummaryText:        true,  // 有条目摘要时优先用摘要
    IncludeNamespaceSummary:   true,  // 追加命名空间概览（确定性生成，不调 LLM）
    NamespaceSummaryMaxTokens: 200,
})
fmt.Println(bundle.Text, bundle.Tokens, bundle.Truncated)
```

- `bundle.Tokens == memory.CountTokens(bundle.Text)`，预算就是"模型能读到多少"。
- 被截断的条目在 `bundle.Items[i].Truncated` 标出，全文仍在库里，可按 ID 取回。
- 宿主可用 `memory.SetTokenCounter(...)` 换成自己模型的精确分词器。
- 命名空间概览由 `RefreshNamespaceSummary` 生成并缓存（内容指纹 + 时效），受命名空间策略的
  `SummaryEnabled` / `SummaryItemTokenThreshold` 约束。

刻意的设计取舍见 [docs/p0-fixes.md](docs/p0-fixes.md)。

## 许可证

MIT
