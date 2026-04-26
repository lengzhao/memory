# 端到端测试用例集

## 测试架构

```
test/
├── e2e_test.go          # 主测试入口
├── db_test.go           # 数据库初始化测试
├── memory_crud_test.go  # 记忆CRUD测试
├── ttl_test.go          # TTL过期测试
├── search_test.go       # 全文搜索测试
├── dedupe_test.go       # 重复检测测试
├── decision_test.go     # 决策引擎测试
├── extract_test.go      # 记忆提取测试
└── concurrent_test.go   # 并发控制测试
```

## 用例清单

### 1. 数据库初始化 (DB Init)
| ID | 用例名称 | 描述 | 优先级 |
|----|---------|------|--------|
| DB-01 | 内存数据库初始化 | 使用 `:memory:` 创建临时数据库并迁移 | P0 |
| DB-02 | 文件数据库初始化 | 创建文件数据库，验证表结构 | P1 |
| DB-03 | FTS5虚拟表创建 | 执行迁移SQL创建FTS5表和触发器 | P0 |

### 2. 记忆CRUD (Memory CRUD)
| ID | 用例名称 | 描述 | 优先级 |
|----|---------|------|--------|
| CRUD-01 | 创建记忆 | Remember存储记忆，验证ID返回 | P0 |
| CRUD-02 | 创建带TTL的记忆 | 设置TTLSeconds，验证ExpiresAt | P0 |
| CRUD-03 | 查询记忆 | Recall按namespace查询 | P0 |
| CRUD-04 | FTS搜索记忆 | Recall按query全文搜索 | P0 |
| CRUD-05 | 标签过滤 | Recall按TagsAny/TagsAll过滤 | P1 |
| CRUD-06 | 更新记忆 | Update修改内容，验证乐观锁 | P0 |
| CRUD-07 | 版本冲突 | 使用错误的ExpectedVersion更新 | P0 |
| CRUD-08 | 软删除 | Forget mode=soft标记删除 | P0 |
| CRUD-09 | 硬删除 | Forget mode=hard物理删除 | P1 |
| CRUD-10 | 过期标记 | Forget mode=expire标记过期 | P1 |
| CRUD-11 | Touch更新访问 | Touch增加访问计数 | P1 |

### 3. TTL管理 (TTL Management)
| ID | 用例名称 | 描述 | 优先级 |
|----|---------|------|--------|
| TTL-01 | 固定TTL过期 | 创建短TTL记忆，等待过期后查询 | P0 |
| TTL-02 | 滑动TTL续期 | TouchWithRenew续期验证 | P1 |
| TTL-03 | 过期清理 | CleanupExpired清理过期项目 | P0 |
| TTL-04 | 包含过期查询 | Recall IncludeExpired=true | P1 |
| TTL-05 | 手动续期 | RenewExpiration更新过期时间 | P1 |

### 4. 重复检测 (Deduplication)
| ID | 用例名称 | 描述 | 优先级 |
|----|---------|------|--------|
| DED-01 | DedupeKey重复 | 相同DedupeKey返回相同ID | P0 |
| DED-02 | 跨命名空间重复 | 不同namespace允许相同DedupeKey | P1 |

### 5. 决策引擎 (Decision Engine)
| ID | 用例名称 | 描述 | 优先级 |
|----|---------|------|--------|
| DEC-01 | 相似记忆查找 | FindSimilarMemories返回相关记忆 | P0 |
| DEC-02 | 决策执行ADD | 执行ADD决策创建记忆 | P0 |
| DEC-03 | 决策执行UPDATE | 执行UPDATE决策更新记忆 | P0 |
| DEC-04 | 决策执行DELETE | 执行DELETE决策删除记忆 | P0 |
| DEC-05 | 决策执行MERGE | 执行MERGE决策合并记忆 | P0 |
| DEC-06 | 完整决策流程 | Extract→FindSimilar→Decide→Execute | P1 |

### 6. 全文搜索 (Full-Text Search)
| ID | 用例名称 | 描述 | 优先级 |
|----|---------|------|--------|
| FTS-01 | 基础FTS查询 | 按关键词搜索返回匹配项 | P0 |
| FTS-02 | 中文搜索 | 中文内容FTS搜索 | P0 |
| FTS-03 | 多词搜索 | 空格分隔多词搜索 | P1 |
| FTS-04 | 无结果查询 | 搜索不存在的关键词返回空 | P1 |

### 7. 并发控制 (Concurrency)
| ID | 用例名称 | 描述 | 优先级 |
|----|---------|------|--------|
| CON-01 | 乐观锁冲突 | 并发更新检测版本冲突 (使用shared cache) | P1 |
| CON-02 | 并发创建 | 并发Remember不冲突 (使用shared cache) | P1 |
| CON-03 | 并发Touch | 并发Touch操作 (使用shared cache) | P1 |
| CON-04 | 读写并发 | Recall与Remember并发执行 (使用shared cache) | P1 |

**注意**: 使用 `file::memory:?cache=shared` 让多个SQLite连接共享同一块内存数据库，解决`:memory:`模式下连接隔离问题。

### 8. 清理维护 (Maintenance)
| ID | 用例名称 | 描述 | 优先级 |
|----|---------|------|--------|
| MTN-01 | 清理已删除项 | PurgeDeleted移动已删除项目 | P1 |
| MTN-02 | 重建FTS索引 | RebuildFTS重建全文索引 | P1 |

## 测试数据约定

- 使用 `:memory:` 数据库避免文件IO
- 每个测试独立初始化数据库
- 使用固定的测试namespace: `test/{test-name}/{uuid}`
- 优先使用mock替代真实LLM调用
