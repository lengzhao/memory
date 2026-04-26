# 端到端测试集 (End-to-End Test Suite)

## 概述

本目录包含memory系统的端到端测试，使用内存SQLite数据库进行快速、隔离的测试执行。

## 测试文件结构

```
test/
├── e2e_test.go           # 测试入口和公共工具函数
├── db_test.go            # 数据库初始化和迁移测试
├── memory_crud_test.go   # 记忆 CRUD 与全文搜索 (FTS5)
├── ttl_test.go           # TTL过期和续期测试
├── dedupe_test.go        # 重复检测测试
├── decision_test.go      # 决策引擎测试
├── concurrent_test.go      # 并发控制（已跳过）
├── concurrent_sqlite_test.go  # 共享 cache 下并发
├── maintenance_test.go   # 维护操作测试
└── e2e_test_cases.md     # 测试用例清单文档
```

## 运行测试

```bash
# 运行所有测试
go test ./test/... -v

# 运行特定测试
go test ./test/... -v -run "TestRemember_CreateMemory"

# 运行特定分类测试
go test ./test/... -v -run "TestTTL_.*"
go test ./test/... -v -run "TestDecision_.*"
```

## 测试覆盖范围

### 1. 数据库初始化 (DB Init)
- 内存数据库初始化
- `memory.Migrate`（GORM `AutoMigrate` + FTS5）验证
- LLM 相关表与 `memory_merges` 验证

### 2. 记忆CRUD (Memory CRUD)
- 创建记忆（带/不带TTL）
- FTS全文搜索
- 标签过滤
- 更新（乐观锁）
- 软删除/硬删除/过期
- 访问统计更新

### 3. TTL管理 (TTL Management)
- 固定TTL过期
- 滑动TTL续期
- 过期清理
- 包含过期查询
- 手动续期

### 4. 重复检测 (Deduplication)
- 同一 `namespace` 下 `dedupe_key` 幂等
- 不同 `namespace` 可重用相同 `dedupe_key` 字符串
- 空 DedupeKey 处理

### 5. 决策引擎 (Decision Engine)
- 相似记忆查找
- ADD/UPDATE/DELETE/MERGE/IGNORE决策执行
- 决策数量匹配验证

### 6. 全文搜索 (Full-Text Search)
- 基础FTS查询（依赖FTS5扩展）

### 7. 并发控制 (Concurrency) - 已跳过
- 并发测试需要文件SQLite支持连接共享
- 在内存数据库中跳过

### 8. 清理维护 (Maintenance)
- 清理已删除项
- 过期项清理
- FTS索引重建

## 注意事项

1. **FTS5依赖**: 部分搜索测试需要SQLite FTS5扩展支持。如果不可用，测试会自动跳过。

2. **内存数据库与并发**: 
   - SQLite `:memory:` 模式下，**每个连接有独立的内存数据库**
   - GORM连接池可能创建多个连接，导致goroutine看到不同的数据库状态
   - **解决方案**: 使用 `file::memory:?cache=shared` 让多个连接共享同一块内存
   - 详见 `concurrent_sqlite_test.go` 中的共享缓存实现

3. **Dedupe 约束**: `namespace` 与 `dedupe_key` 的复合唯一，由 GORM 模型与 `AutoMigrate` 管理。

4. **时间敏感测试**: TTL相关测试使用实际时间等待，因此执行时间较长（约2秒/测试）。

## 并发测试说明

### 为什么 `:memory:` 不能用于并发测试？

```
SQLite :memory: 模式
├── 连接A: 内存区域A (独立)
├── 连接B: 内存区域B (独立)  
└── 连接C: 内存区域C (独立)

问题: GORM连接池可能让不同goroutine使用不同连接
结果: "no such table" 错误
```

### 解决方案: `file::memory:?cache=shared`

```
SQLite shared cache 模式
├── 连接A ──┐
├── 连接B ──┼──> 共享内存区域
└── 连接C ──┘

所有连接看到同一块内存数据库
```

**实现代码**（与 `concurrent_sqlite_test.go` 一致，并带 `busy_timeout`）:
```go
dsn := "file::memory:?cache=shared&_pragma=busy_timeout(30000)&_pragma=foreign_keys(1)"
db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
```
