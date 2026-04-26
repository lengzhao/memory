# memory

一个简单的基于 SQLite 的记忆系统，用于给 AI Agent 内嵌使用。

## 技术栈

- **Go** + **GORM**
- **SQLite** (FTS5 全文搜索)
- 本地优先、单用户、低运维

## 快速开始

### 基础功能（无需API Key）

```bash
# 初始化数据库（在仓库根目录执行）
go run examples/07_server_init/main.go

# 数据库文件位置（默认工作目录下）
./memory.db
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
export OPENAI_MODEL=gpt-4o        # 默认模型 (gpt-4o/gpt-4/gpt-3.5-turbo)
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
├── examples/           # 可执行示例（含 DB 初始化、LLM 完整演示等）
│   ├── 07_server_init/ # 最简：Migrate + 插入/查询/FTS
│   └── 08_extract_demo/# 多轮对话 LLM 提取
├── model/              # GORM 模型（表结构单一来源）
│   └── memory.go
├── service/            # 业务逻辑（Memory、提取、策略、决策等）
├── store/              # DB 连接与 schema：`Migrate` = AutoMigrate + FTS5
│   ├── db.go
│   └── fts.go          # FTS5 虚表与触发器（GORM 无法表达的部分）
├── docs/
│   └── memory.md       # 详细技术方案
├── go.mod
└── README.md
```

**Schema**：仅通过 `memory.Migrate(db)`（或 `store.Migrate`）初始化。GORM `AutoMigrate` 创建业务表；`store` 内建 FTS5 虚表与同步触发器。提取 **Prompt 可不建表行**：未配置 `is_default` 的 `extraction_prompts` 时，`Extract` 使用 `service` 包内建默认（`dialog_extractions.prompt_id` 记为 `prompt-default-v1`）。需要定制时在 DB 中新增/设置 `is_default` 即可。不使用独立 SQL 迁移目录。

> **集成说明**：`import "github.com/lengzhao/memory"` 即可；也可按需引用子包 `model`、`service`、`store`。

## 核心特性 v0.3

- ✅ 多 namespace 分层存储 (transient/profile/action/knowledge)
- ✅ **并发控制**：乐观锁 (`version` 字段)
- ✅ **幂等写入**：`dedupe_key`（同一 namespace 内唯一）等
- ✅ **TTL 策略**：fixed / sliding / manual 三种模式
- ✅ **可恢复删除**：软删后保留在 `deleted_items` 表
- ✅ **全文搜索**：FTS5 虚表 + 自动同步触发器
- ✅ **策略持久化**：`namespace_policies` 表存储配置
- ✅ **事件追踪**：审计日志 (`memory_events`)
- ✅ **LLM 集成**：多 Provider（OpenAI/Claude/Ollama）
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
| `llm_configs` | LLM Provider 配置（`api_key` 由调用方自行加解密后存储） |
| `extraction_prompts` | 提取 Prompt 模板 |
| `dialog_extractions` | 对话提取记录（幂等检测） |

详见 `model/memory.go` 与 `docs/memory.md`。

## 许可证

MIT
