# Memory系统使用示例

本目录包含memory系统的使用示例，每个示例在独立的子文件夹中。

## 目录结构

```
examples/
├── 01_basic/            # 基础CRUD
├── 02_dedupe/           # 去重
├── 03_ttl/              # TTL
├── 04_callback/         # 回调
├── 05_extract/          # LLM 提取（子包与 facade 示例）
├── 06_policy/           # Namespace 策略
├── 07_server_init/      # 最简：InitDB、Migrate、GORM/FTS 直查
├── 08_extract_demo/     # 完整多轮 LLM 提取 + 落库（需 OPENAI_API_KEY 等）
└── README.md
```

各示例在启动时调用 `memory.InitDB` 与 `memory.Migrate`；表结构仅依赖 GORM，无需执行额外 SQL 文件。

## 运行示例

```bash
# 进入examples目录
cd examples

# 运行各个示例
go run 01_basic/main.go
go run 02_dedupe/main.go
go run 03_ttl/main.go
go run 04_callback/main.go
go run 05_extract/main.go
go run 06_policy/main.go
go run 07_server_init/main.go
go run 08_extract_demo/main.go
```

## 示例说明

### 01_basic - 基础使用
展示核心CRUD操作：
- 初始化数据库
- 存储记忆（Remember）
- 全文搜索（Recall with Query）
- 标签过滤（Recall with Tags）
- 更新记忆（Update）
- 触摸记忆（Touch）
- 软删除（Forget）
- TTL自动过期

### 02_dedupe - 重复检测
展示如何使用dedupe_key防止重复：
- 使用外部ID作为去重键
- 多次导入相同数据时自动去重
- 不同数据源的独立去重

### 03_ttl - TTL生存时间
展示TTL的各种用法：
- 创建带TTL的记忆
- 自动过期清理
- 手动续期（RenewExpiration）
- 滑动TTL（TouchWithRenew）

### 04_callback - 生命周期回调
展示如何使用回调函数：
- OnCreated: 创建时触发
- OnUpdated: 更新时触发
- OnDeleted: 删除时触发
- OnExpired: 过期时触发

### 05_extract - LLM记忆提取
展示如何使用Extractor：
- 配置LLM（OpenAI）
- 配置提取提示模板
- 从对话中提取记忆
- 决策引擎简介

### 06_policy - Namespace策略管理
展示如何使用PolicyManager：
- 查看各namespace类型的默认策略
- 为特定namespace设置自定义策略（TTL、排序权重）
- 策略继承和回退机制
- 精确匹配 vs 类型默认

### 07_server_init - 数据库初始化最简流
- `store.InitDB` / `store.Migrate`、插入示例条、GORM 查询、FTS5 与策略写入（不依赖 `memory` 根包，便于看底层流）

### 08_extract_demo - 完整 LLM 提取演示
- 多段对话、真实调用 Extractor 落库、打印 `dialog_extractions` 与全表记忆
- 示例会**可选**写入 demo 用 `llm_configs`；提取 **Prompt** 不必落库（由库内建默认）
- 若完全自建 LLM 行，可设 `EXTRACT_DEMO_NO_SEED=1`
- 需 `OPENAI_API_KEY` 等环境变量

## 示例输出预览

### 01_basic 输出预览

```
=== 1. 初始化数据库 ===
数据库初始化完成

=== 2. 存储用户偏好 ===
存储用户偏好，ID: 01HXY...

=== 3. 存储待办任务 ===
存储任务，ID: 01HXY...

...
```

### 06_policy 输出预览

```
=== Namespace策略管理示例 ===

1. 查看各namespace类型的默认策略：
   [transient]
     TTL: 259200秒 (sliding)
     排序权重: FTS=0.55, 新鲜度=0.20, 重要性=0.15, 置信度=0.10

   [profile]
     TTL: 永不过期
     排序权重: FTS=0.55, 新鲜度=0.15, 重要性=0.20, 置信度=0.10

...
```

## 注意事项

1. **数据库文件**: 示例会创建 `.db` 文件在各示例目录下，运行后可以删除
2. **API密钥**: `05_extract` 需要有效的OpenAI API密钥才能进行真实提取（演示模式可运行）
