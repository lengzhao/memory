# memory

一个简单的基于 SQLite 的记忆系统，用于给 AI Agent 内嵌使用。

## 技术栈

- **Go** + **GORM**
- **SQLite** (FTS5 全文搜索)
- 本地优先、单用户、低运维

## 快速开始

### 基础功能（无需API Key）

```bash
# 初始化数据库
go run cmd/server/main.go

# 数据库文件位置
./memory.db
```

### LLM自动提取（使用OpenAI API）

```bash
# 1. 设置环境变量
export OPENAI_API_KEY=sk-your-api-key

# 2. 运行演示
go run cmd/extract_demo/main.go
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
go run cmd/extract_demo/main.go
```

## 自动提取演示

```bash
$ go run cmd/extract_demo/main.go

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
├── cmd/
│   ├── server/         # 主程序入口
│   └── extract_demo/   # LLM提取演示程序
├── model/              # GORM 模型定义（对外暴露）
│   └── memory.go       # 所有表结构
├── service/            # 业务逻辑服务
│   └── extractor.go    # LLM提取服务
├── store/              # 数据库初始化和迁移
│   └── db.go           # GORM 配置
├── migrations/         # SQL 迁移文件
│   └── 001_initial_schema.sql  # FTS5 虚拟表 + 触发器
├── docs/
│   └── memory.md       # 详细技术方案 v0.3
├── go.mod
└── README.md
```

> **集成说明**：其他项目引用本模块时，只需 `import "github.com/lengzhao/memory"` 即可使用所有功能。也可按需引用子包 `model`、`service`、`store`。

## 核心特性 v0.3

- ✅ 多 namespace 分层存储 (transient/profile/action/knowledge - 简化4类)
- ✅ **并发控制**：乐观锁 (`version` 字段)
- ✅ **幂等写入**：`dedupe_key` + `request_id` 防重复
- ✅ **TTL 策略**：fixed / sliding / manual 三种模式
- ✅ **可恢复删除**：软删后保留在 `deleted_items` 表
- ✅ **全文搜索**：FTS5 虚表 + 自动同步触发器
- ✅ **策略持久化**：`namespace_policies` 表存储配置
- ✅ **事件追踪**：完整审计日志 (`memory_events`)
- ✅ **🆕 LLM集成**：支持配置多个 Provider（OpenAI/Claude/Ollama）
- ✅ **🆕 自动提取**：对话内容自动分类到 6 类 namespace
- ✅ **🆕 智能分类**：自动识别 transient/profile/action/knowledge

## 数据模型

### 主要表

| 表名 | 说明 |
|------|------|
| `memory_items` | 核心记忆条目 |
| `memory_links` | 记忆间关系（支持/contradicts/derived_from等） |
| `namespace_summaries` | 命名空间摘要 |
| `namespace_policies` | 命名空间策略配置 |
| `memory_events` | 审计事件日志 |
| `deleted_items` | 软删恢复表 |
| `fts_memory` | FTS5 全文搜索虚表 |
| `llm_configs` | LLM Provider 配置（API key加密存储） |
| `extraction_prompts` | 提取 Prompt 模板 |
| `dialog_extractions` | 对话提取记录（幂等检测） |

详见 `internal/model/memory.go` 和 `docs/memory.md`。

## 许可证

MIT
