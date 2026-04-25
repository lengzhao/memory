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
//   - Multi-namespace storage (session, profile, task, kb, skills, sop)
//   - Full-text search with FTS5
//   - TTL/expiration management (fixed, sliding, manual)
//   - Optimistic locking for concurrent updates
//   - Idempotent writes with dedupe_key
//   - LLM-based automatic memory extraction
//
package memory

import (
	"github.com/lengzhao/memory/model"
	"github.com/lengzhao/memory/pkg/errors"
	"github.com/lengzhao/memory/pkg/llm"
	"github.com/lengzhao/memory/service"
	"github.com/lengzhao/memory/store"
)

// Re-export all types from sub-packages for easy access

// Model types
type (
	// Core memory types
	MemoryItem          = model.MemoryItem
	MemoryLink          = model.MemoryLink
	NamespaceSummary    = model.NamespaceSummary
	NamespacePolicy     = model.NamespacePolicy
	MemoryEvent         = model.MemoryEvent
	DeletedItem         = model.DeletedItem
	FTSMemory           = model.FTSMemory

	// LLM integration types
	LLMConfig           = model.LLMConfig
	ExtractionPrompt    = model.ExtractionPrompt
	DialogExtraction    = model.DialogExtraction

	// Enum types
	NamespaceType       = model.NamespaceType
	ItemStatus          = model.ItemStatus
	SourceType          = model.SourceType
	LinkType            = model.LinkType
	TTLPolicy           = model.TTLPolicy
	EventType           = model.EventType
	LLMProvider         = model.LLMProvider
	ExtractionStatus    = model.ExtractionStatus
)

// Enum constants
const (
	// Namespace types (simplified 4 categories)
	NamespaceTransient = model.NamespaceTypeTransient  // Short-term context
	NamespaceProfile   = model.NamespaceTypeProfile    // User preferences
	NamespaceAction    = model.NamespaceTypeAction     // Tasks/todos
	NamespaceKnowledge = model.NamespaceTypeKnowledge  // Facts/skills

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
	ProviderOpenAI   = model.LLMProviderOpenAI
	ProviderClaude   = model.LLMProviderClaude
	ProviderOllama   = model.LLMProviderOllama
	ProviderCustom   = model.LLMProviderCustom
)

// Service types
type (
	Extractor         = service.Extractor
	ExtractRequest      = service.ExtractRequest
	ExtractedMemory     = service.ExtractedMemory
	ExtractResult       = service.ExtractResult
)

// Store types and functions
type (
	Config = store.Config
)

// Database functions
var (
	// Configuration
	DefaultConfig = store.DefaultConfig

	// Initialization
	InitDB = store.InitDB
	Close  = store.Close

	// Migration
	Migrate            = store.Migrate
	ExecMigrationFile  = store.ExecMigrationFile
)

// Service types
type (
	MemoryService     = service.MemoryService
	ServiceConfig     = service.Config
	RememberRequest   = service.RememberRequest
	RecallRequest     = service.RecallRequest
	MemoryHit         = service.MemoryHit
	ForgetRequest     = service.ForgetRequest
	UpdateRequest     = service.UpdateRequest
	SummaryGenerator  = service.SummaryGenerator
	PolicyManager     = service.PolicyManager
)

// Error types
var (
	ErrNotFound     = errors.ErrNotFound
	ErrConflict     = errors.ErrConflict
	ErrDuplicate    = errors.ErrDuplicate
	ErrValidation   = errors.ErrValidation
	ErrUnauthorized = errors.ErrUnauthorized
	ErrLLM          = errors.ErrLLM
)

// LLM types
type (
	LLMClient         = llm.Client
	LLMCompletionReq  = llm.CompletionRequest
	LLMCompletionResp  = llm.CompletionResponse
	LLMExtractMemory   = llm.ExtractMemory
	LLMExtractResult   = llm.ExtractResult
	LLMExtractor       = llm.Extractor
)

// LLM functions
var (
	NewLLMExtractor    = llm.NewExtractor
	NewOpenAIClient    = llm.NewOpenAIClient
)

// Service functions
var (
	// Create new service/extractor instances
	NewMemoryService         = service.NewMemoryService
	NewMemoryServiceWithConfig = service.NewMemoryServiceWithConfig
	NewSummaryGenerator      = service.NewSummaryGenerator
	NewPolicyManager         = service.NewPolicyManager
	NewExtractor             = service.NewExtractor
)

// Utility functions
var (
	// From model package
	GenerateID = model.GenerateID
)
