# Memory 系统改进任务清单

> **架构定位**: 这是一个嵌入式 Go 库（embedded library），直接通过代码 API 集成到 Agent 或应用程序中，无需独立部署或 HTTP 服务。
>
> **使用方式**:
> ```go
> db, _ := memory.InitDB(cfg)
> svc := memory.NewService(db)
> itemID, _ := svc.Remember(ctx, req)
> results, _ := svc.Recall(ctx, query)
> ```

---

## P0 - 核心架构（立即执行）

### 1. 实现核心 MemoryService ✅
- **状态**: 已完成
- **描述**: 创建 `service/memory.go`，封装 Remember/Recall/Forget 核心用例，作为库的主入口
- **验收标准**:
  - [ ] `Remember(ctx, req) (string, error)` - 幂等写入，返回 item_id
  - [ ] `Recall(ctx, req) ([]MemoryHit, error)` - 支持 FTS + 过滤 + 排序 + 评分解释
  - [ ] `Forget(ctx, req) (int, error)` - 软删/硬删/过期标记
  - [ ] `Update(ctx, req) error` - 乐观锁更新，返回 ErrConflict
  - [ ] `Touch(ctx, itemID) error` - 更新访问统计（用于 Sliding TTL）
  - [ ] 单元测试覆盖 > 80%
- **参考文档**: `docs/memory.md` 第 8 章

### 2. 替换 GenerateID 实现 ✅
- **状态**: 已完成
- **描述**: 使用标准 ULID 库替换当前时间戳实现
- **验收标准**:
  - [ ] 引入 `github.com/oklog/ulid` 或 `github.com/google/uuid`
  - [ ] 高并发测试通过（1000 goroutines 无冲突）
  - [ ] 保持 ID 可读性（使用 ULID 而非 UUID）
- **修改文件**: `model/memory.go`

### 3. API Key 配置简化 ✅
- **状态**: 已完成
- **描述**: 完全由调用方负责密钥管理，库只接收配置值，不做任何额外处理
- **验收标准**:
  - [ ] `LLMConfig` 的 `APIKey` 仅接受显式传入的值
  - [ ] 库不读取任何环境变量（如 `OPENAI_API_KEY`）
  - [ ] 调用方负责从 env/config/vault 等读取并传入
  - [ ] APIKey 为空时返回清晰错误，不尝试自动获取
  - [ ] 删除 `pkg/crypto/` 相关计划
- **依赖**: P0-1

---

## P1 - 扩展能力（近期）

### 4. 抽象 LLMClient 接口（分阶段）✅
- **状态**: 已完成 (v1 - OpenAI)
- **描述**: 解耦 Extractor 与具体 LLM 实现，v1 仅实现 OpenAI，接口预留扩展
- **验收标准（v1）**:
  - [ ] 定义 `pkg/llm/client.go` 接口
  - [ ] 实现 `OpenAIClient`（当前已有逻辑提取）
  - [ ] Extractor 依赖接口而非具体实现
  - [ ] 通过配置切换 Provider（当前仅支持 openai）
- **后续版本**:
  - v2: 添加 ClaudeClient
  - v3: 添加 OllamaClient

### 5. 实现 Sliding TTL 机制 ✅
- **状态**: 已完成
- **描述**: 访问续期模式，高频访问的记忆自动延长过期时间
- **验收标准**:
  - [ ] `Touch(itemID)` 接口更新访问计数和时间
  - [ ] 访问计数达到阈值时自动续期 `expires_at`
  - [ ] 最大续期次数限制（默认 10 次）
  - [ ] 集成到 Recall 流程（读后触发续期检查）
- **参考**: 文档第 5.1.1 节

### 6. 数据库索引优化 ✅
- **状态**: 已完成
- **描述**: 评估并添加必要的复合索引，外键约束由应用层保证（SQLite 外键有性能开销）
- **验收标准**:
  - [ ] 评估复合索引：`namespace + status + expires_at`
  - [ ] 评估复合索引：`namespace_type + created_at`
  - [ ] 性能测试：10万条数据下查询 < 150ms
  - [ ] 文档说明：外键关系由调用方保证（嵌入式场景数据一致性由应用控制）

### 7. 错误处理统一 ✅
- **状态**: 已完成
- **描述**: 定义错误类型，统一错误处理策略
- **验收标准**:
  - [ ] 定义 `pkg/errors/` 包，包含:
    - `ErrConflict` (乐观锁冲突)
    - `ErrNotFound`
    - `ErrDuplicate`
    - `ErrValidation`
  - [ ] Extractor 错误返回而非打印
  - [ ] 错误包含结构化信息（错误码、重试建议等）

---

## P2 - 完整功能（中期）

### 8. Summary 生成接口 ✅
- **状态**: 已完成
- **文件**: `service/summary.go`
- **验收标准**:
  - [x] `GenerateItemSummary(ctx, itemID string) error` - 为单条记忆生成摘要
  - [x] `GenerateNamespaceSummary(ctx, namespace string) (string, error)` - 生成命名空间摘要
  - [x] 文档说明：何时调用由应用层决定（如写入长内容后、定时任务中）

### 9. 过期清理接口 ✅
- **状态**: 已完成
- **验收标准**:
  - [x] `CleanupExpired(ctx) (int, error)` - 清理过期项目，返回清理数量
  - [x] `PurgeDeleted(ctx, before time.Time) (int, error)` - 物理删除软删项目
  - [x] 支持策略配置：`soft`（标记过期）/`hard`（立即物理删除）
  - [x] 文档示例：如何在应用层定期调用（如每小时、启动时）

### 10. Namespace Policy 简化 ✅
- **状态**: 已完成
- **文件**: `service/policy.go`
- **验收标准**:
  - [x] `GetPolicy(namespace string) (NamespacePolicy, error)` - 精确匹配查询
  - [x] 无精确策略时，返回对应 `namespace_type` 的默认策略
  - [x] 移除前缀匹配复杂度（如 `action/projA/*`），降低实现难度

### 11. FTS 重建接口（应急）✅
- **状态**: 已完成
- **验收标准**:
  - [x] `RebuildFTS(ctx) error` - 手工全量重建（应急使用）
  - [x] 移除自动重建逻辑，避免意外阻塞
  - [x] 移除校验逻辑，依赖 SQLite trigger 保证同步

### 12. 生命周期回调（简化事件）✅
- **状态**: 已完成
- **验收标准**:
  - [x] `Config` 支持可选回调：`OnCreated`, `OnUpdated`, `OnDeleted`, `OnExpired`
  - [x] 回调签名：`func(ctx context.Context, item MemoryItem)` 或 `func(itemID string)`
  - [x] `memory_events` 表继续记录（用于审计）
  - [x] `NewMemoryServiceWithConfig()` 创建带回调的服务

---

## 技术债务

### v2 待办（暂不实现）
- **硬编码配置清理**: 成本计算、API URL、置信度阈值等（v1 可用即可）
- **更多 LLM Provider**: ClaudeClient、OllamaClient
- **前缀匹配策略**: `action/projA/*` 等通配符策略

### TD2. Context 使用完善
- [ ] 所有 DB 操作传入 `ctx` 支持超时
- [ ] HTTP Client 使用 `context.WithTimeout`

### TD3. 日志简化
- [ ] 移除库内日志打印，通过错误返回让调用方处理
- [ ] 或支持传入简单 `Logger` 接口（避免强制依赖 slog/zap）
- [ ] 示例：`type Logger interface { Printf(format string, v ...interface{}) }`

---

## 项目结构目标

```
memory/
├── memory.go              # 主入口（re-export + 简化 API）
├── service/
│   ├── memory.go          # MemoryService 核心实现 (P0-1)
│   ├── memory_test.go     # 核心服务测试
│   ├── extractor.go       # LLM 提取器（已有）
│   ├── summary.go         # SummaryGenerator (P2-8)
│   ├── summary_test.go    # Summary 测试
│   ├── policy.go          # PolicyManager (P2-10)
│   └── policy_test.go     # Policy 测试
├── store/
│   ├── db.go              # 数据库初始化
│   └── testutil.go        # 测试工具（内存数据库）
├── model/
│   ├── memory.go          # 数据模型
│   └── memory_test.go     # 模型测试（ULID 等）
├── pkg/
│   ├── llm/               # LLM 客户端抽象 (P1-4)
│   │   ├── client.go
│   │   ├── client_test.go
│   │   └── openai.go
│   └── errors/            # 错误定义 (P1-7)
│       ├── errors.go
│       └── errors_test.go
│   # 注：加密由调用方负责，库不提供 crypto 包
├── migrations/            # 数据库迁移（已有）
└── docs/
    └── memory.md          # 架构文档
```

---

## 当前状态速览

| 里程碑 | 进度 | 关键阻塞 |
|--------|------|----------|
| P0-1 核心服务 | ✅ 100% | - |
| P0-2 ID 生成 | ✅ 100% | - |
| P0-3 配置简化 | ✅ 100% | - |
| P1-4 LLM 接口 | ✅ 100% | - |
| P1-5 Sliding TTL | ✅ 100% | - |
| P1-6 索引优化 | ✅ 100% | - |
| P1-7 错误处理 | ✅ 100% | - |
| P2-8 Summary | ✅ 100% | - |
| P2-9 过期清理 | ✅ 100% | - |
| P2-10 Policy | ✅ 100% | - |
| P2-11 FTS 重建 | ✅ 100% | - |
| P2-12 回调 | ✅ 100% | - |
| 测试覆盖 | ✅ 完成 | - |

---

## 开发建议

1. **分支策略**: 每个 P0/P1 任务独立分支，完成后 PR 合并
2. **测试要求**: 每个功能必须包含单元测试 + 集成测试
3. **文档同步**: 代码变更同步更新 `docs/memory.md` 和 `README.md`
4. **兼容性**: 数据库 schema 变更需新建 migration 文件
5. **接口稳定性**: P0 完成后冻结主 API，后续版本保持向后兼容
