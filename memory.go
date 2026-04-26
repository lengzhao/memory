// Package memory provides a simple SQLite-based memory system for AI Agent.
//
// Quick Start:
//
//	db, err := memory.InitDB(memory.DefaultConfig())
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer memory.Close(db)
//
//	if err := memory.Migrate(db); err != nil {
//	    log.Fatal(err)
//	}
//
//	// Use the database
//	item := memory.MemoryItem{
//	    ID:      memory.GenerateID(),
//	    Content: "Hello, World!",
//	    // ... other fields
//	}
//	db.Create(&item)
//
// Features:
//   - Multi-namespace storage (transient, profile, action, knowledge)
//   - Full-text search with FTS5
//   - TTL/expiration management (fixed, sliding, manual)
//   - Optimistic locking for concurrent updates
//   - Idempotent writes with dedupe_key
//   - LLM-based automatic memory extraction
package memory

import (
	"github.com/lengzhao/memory/model"
	"github.com/lengzhao/memory/service"
	"github.com/lengzhao/memory/store"
)

// Re-export all types from sub-packages for easy access

// Model types
type (
	// Core memory types
	MemoryItem      = model.MemoryItem
	NamespacePolicy = model.NamespacePolicy

	// LLM / extraction (DB 模型，常用者保留在根包)
	ExtractionPrompt = model.ExtractionPrompt

	// Enum types
	NamespaceType = model.NamespaceType
	ItemStatus    = model.ItemStatus
	SourceType    = model.SourceType
	TTLPolicy     = model.TTLPolicy
	LLMProvider   = model.LLMProvider
)

// Enum constants
const (
	// Namespace types (simplified 4 categories)
	NamespaceTransient = model.NamespaceTypeTransient // Short-term context
	NamespaceProfile   = model.NamespaceTypeProfile   // User preferences
	NamespaceAction    = model.NamespaceTypeAction    // Tasks/todos
	NamespaceKnowledge = model.NamespaceTypeKnowledge // Facts/skills

	// Item statuses
	StatusActive   = model.ItemStatusActive
	StatusExpired  = model.ItemStatusExpired
	StatusArchived = model.ItemStatusArchived
	StatusDeleted  = model.ItemStatusDeleted

	// Source types
	SourceUser   = model.SourceTypeUser
	SourceAgent  = model.SourceTypeAgent
	SourceImport = model.SourceTypeImport
	SourceSystem = model.SourceTypeSystem

	// TTL policies
	TTLFixed   = model.TTLPolicyFixed
	TTLSliding = model.TTLPolicySliding
	TTLManual  = model.TTLPolicyManual

	// LLM providers
	ProviderOpenAI = model.LLMProviderOpenAI
	ProviderClaude = model.LLMProviderClaude
	ProviderOllama = model.LLMProviderOllama
	ProviderCustom = model.LLMProviderCustom

	// Decision types
	DecisionAdd    = service.DecisionAdd
	DecisionUpdate = service.DecisionUpdate
	DecisionDelete = service.DecisionDelete
	DecisionIgnore = service.DecisionIgnore
	DecisionMerge  = service.DecisionMerge
)

// Store types and functions
type (
	Config = store.Config
)

// Database functions
var (
	// Configuration
	DefaultConfig = store.DefaultConfig

	// Initialization (set Config.AutoMigrate=true to run migration automatically)
	InitDB = store.InitDB
	Close  = store.Close
)

// Service types
type (
	MemoryService    = service.MemoryService
	ServiceConfig    = service.Config
	RememberRequest  = service.RememberRequest
	RecallRequest    = service.RecallRequest
	ListRequest      = service.ListRequest
	MemoryHit        = service.MemoryHit
	ForgetRequest    = service.ForgetRequest
	UpdateRequest    = service.UpdateRequest
	PolicyManager    = service.PolicyManager
	DecisionEngine   = service.DecisionEngine
	DecisionType     = service.DecisionType
	DecisionRequest  = service.DecisionRequest
	DecisionResult   = service.DecisionResult
	MemoryDecision   = service.MemoryDecision
	SimilarMemory    = service.SimilarMemory
	ExecutionResult  = service.ExecutionResult
	Extractor        = service.Extractor
	ExtractRequest   = service.ExtractRequest
	ExtractedMemory  = service.ExtractedMemory
	ExtractResult = service.ExtractResult
)

// Error types（实现位于 service 包，根包重导出以单 import 使用）
type (
	MemoryError = service.MemoryError
	ErrorCode   = service.ErrorCode
)

const (
	CodeNotFound     = service.CodeNotFound
	CodeConflict     = service.CodeConflict
	CodeDuplicate    = service.CodeDuplicate
	CodeValidation   = service.CodeValidation
	CodeUnauthorized = service.CodeUnauthorized
	CodeLLM          = service.CodeLLM
	CodeInternal     = service.CodeInternal
)

var (
	ErrNotFound     = service.ErrNotFound
	ErrConflict     = service.ErrConflict
	ErrDuplicate    = service.ErrDuplicate
	ErrValidation   = service.ErrValidation
	ErrUnauthorized = service.ErrUnauthorized
	ErrLLM          = service.ErrLLM

	// 结构化错误构造与解析
	ErrorNew  = service.ErrorNew
	ErrorWrap = service.ErrorWrap
	ErrorAs   = service.ErrorAs
)

// Service functions
var (
	// Create new service/extractor instances
	NewMemoryService  = service.NewMemoryService
	NewPolicyManager  = service.NewPolicyManager
	NewExtractor      = service.NewExtractor
	NewDecisionEngine = service.NewDecisionEngine

	// QuickExtract is a convenience function for code-only extraction.
	// Pass LLM config directly without DB setup.
	QuickExtract = service.QuickExtract
)

// Logging allows users to configure their own slog handler.
// Example:
//
//	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
//	memory.SetLogger(slog.New(handler))
var (
	SetLogger = service.SetLogger
	GetLogger = service.GetLogger
)

// 下列符号在 service 包定义，根包再导出便于单 import。

// BuiltinExtractionPrompt 为内建记忆提取模板（与 service 包一致）。
var BuiltinExtractionPrompt = service.BuiltinExtractionPrompt

// Utility functions
var (
	// From model package
	GenerateID = model.GenerateID
)
