// Package model defines GORM models for the memory system.
// All timestamps use time.Time and will be stored as DATETIME in SQLite.
package model

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
)

// NamespaceType defines the type of namespace
// Simplified to 4 categories for better usability
type NamespaceType string

const (
	// Transient: Short-term conversation context, expires quickly
	NamespaceTypeTransient NamespaceType = "transient"

	// Profile: User preferences, personal info, long-term stable
	NamespaceTypeProfile NamespaceType = "profile"

	// Action: Tasks, todos, actionable items
	NamespaceTypeAction NamespaceType = "action"

	// Knowledge: Facts, skills, procedures, learned information
	NamespaceTypeKnowledge NamespaceType = "knowledge"
)

// ItemStatus defines the status of a memory item
type ItemStatus string

const (
	ItemStatusActive   ItemStatus = "active"
	ItemStatusExpired  ItemStatus = "expired"
	ItemStatusArchived ItemStatus = "archived"
	ItemStatusDeleted  ItemStatus = "deleted"
)

// SourceType defines the source of a memory item
type SourceType string

const (
	SourceTypeUser   SourceType = "user"
	SourceTypeAgent  SourceType = "agent"
	SourceTypeImport SourceType = "import"
	SourceTypeSystem SourceType = "system"
)

// LinkType defines the type of relationship between memory items
type LinkType string

const (
	LinkTypeSupports    LinkType = "supports"
	LinkTypeContradicts LinkType = "contradicts"
	LinkTypeDerivedFrom LinkType = "derived_from"
	LinkTypeRelatedTo   LinkType = "related_to"
	LinkTypeSupersedes  LinkType = "supersedes"
)

// TTLPolicy defines the TTL policy type
type TTLPolicy string

const (
	TTLPolicyFixed   TTLPolicy = "fixed"
	TTLPolicySliding TTLPolicy = "sliding"
	TTLPolicyManual  TTLPolicy = "manual"
)

// EventType defines the type of memory event
type EventType string

const (
	EventTypeCreate           EventType = "create"
	EventTypeUpdate           EventType = "update"
	EventTypeRead             EventType = "read"
	EventTypeExpire           EventType = "expire"
	EventTypeDelete           EventType = "delete"
	EventTypeSummarize        EventType = "summarize"
	EventTypeRestore          EventType = "restore"
	EventTypeConflictDetected EventType = "conflict_detected"
)

// MemoryItem represents a single memory entry in the system.
// This is the core entity for storing agent memories.
type MemoryItem struct {
	ID            string        `gorm:"column:id;type:text;primaryKey"`
	Namespace     string        `gorm:"column:namespace;type:text;not null;index:idx_mem_namespace;index:idx_mem_recall_filter;uniqueIndex:idx_namespace_dedupe"`
	NamespaceType NamespaceType `gorm:"column:namespace_type;type:text;not null;index:idx_mem_ns_type;index:idx_mem_type_created"`
	Title         string        `gorm:"column:title;type:text"`
	Content       string        `gorm:"column:content;type:text;not null"`
	Summary       string        `gorm:"column:summary;type:text"`
	TagsJSON      string        `gorm:"column:tags_json;type:text"` // JSON array stored as string
	SourceType    SourceType    `gorm:"column:source_type;type:text"`
	SourceRef     string        `gorm:"column:source_ref;type:text"`
	Importance    int           `gorm:"column:importance;type:integer;default:0"`
	Confidence    float64       `gorm:"column:confidence;type:real;default:1.0"`
	Status        ItemStatus    `gorm:"column:status;type:text;default:'active';index:idx_mem_status;index:idx_mem_recall_filter"`
	ExpiresAt     *time.Time    `gorm:"column:expires_at;type:datetime;index:idx_mem_expires;index:idx_mem_recall_filter"`
	CreatedAt     time.Time     `gorm:"column:created_at;type:datetime;not null;index:idx_mem_created;index:idx_mem_type_created"`
	UpdatedAt     time.Time     `gorm:"column:updated_at;type:datetime;not null"`
	LastAccessAt  *time.Time    `gorm:"column:last_access_at;type:datetime;index:idx_mem_last_access"`
	AccessCount   int           `gorm:"column:access_count;type:integer;default:0;index:idx_mem_access"`
	Version       int           `gorm:"column:version;type:integer;default:1"` // Optimistic locking
	DedupeKey     *string       `gorm:"column:dedupe_key;type:text;uniqueIndex:idx_namespace_dedupe"`
	EmbeddingRef  *string       `gorm:"column:embedding_ref;type:text"` // Reserved for future vector support
}

// TableName specifies the table name for MemoryItem
func (MemoryItem) TableName() string {
	return "memory_items"
}

// MemoryLink represents relationships between memory items.
// Supports various link types including contradiction detection.
type MemoryLink struct {
	ID         string    `gorm:"column:id;type:text;primaryKey"`
	FromID     string    `gorm:"column:from_id;type:text;not null;index:idx_link_from"`
	ToID       string    `gorm:"column:to_id;type:text;not null;index:idx_link_to"`
	LinkType   LinkType  `gorm:"column:link_type;type:text;not null;index:idx_link_type"`
	Weight     float64   `gorm:"column:weight;type:real;default:1.0"`
	CreatedAt  time.Time `gorm:"column:created_at;type:datetime;not null"`
	ReasonJSON *string   `gorm:"column:reason_json;type:text"` // JSON with link reason/confidence details
}

// TableName specifies the table name for MemoryLink
func (MemoryLink) TableName() string {
	return "memory_links"
}

// NamespaceSummary stores aggregated summaries for namespaces.
// Used for quick overview of namespace contents without loading all items.
type NamespaceSummary struct {
	ID          string     `gorm:"column:id;type:text;primaryKey"`
	Namespace   string     `gorm:"column:namespace;type:text;uniqueIndex;not null"`
	Summary     string     `gorm:"column:summary;type:text;not null"`
	ItemCount   int        `gorm:"column:item_count;type:integer;not null"`
	WindowStart *time.Time `gorm:"column:window_start;type:datetime"`
	WindowEnd   *time.Time `gorm:"column:window_end;type:datetime"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;type:datetime;not null"`
}

// TableName specifies the table name for NamespaceSummary
func (NamespaceSummary) TableName() string {
	return "namespace_summaries"
}

// NamespacePolicy stores configuration policies for namespaces.
// Supports exact match and prefix match (e.g., "task/projA/*").
type NamespacePolicy struct {
	Namespace                 string    `gorm:"column:namespace;type:text;primaryKey"`
	TTLSeconds                *int      `gorm:"column:ttl_seconds;type:integer"` // nil means never expires
	TTLPolicy                 TTLPolicy `gorm:"column:ttl_policy;type:text;default:'fixed'"`
	SlidingTTLThreshold       int       `gorm:"column:sliding_ttl_threshold;type:integer;default:3"`
	SummaryEnabled            bool      `gorm:"column:summary_enabled;type:boolean;default:1"`
	SummaryItemTokenThreshold int       `gorm:"column:summary_item_token_threshold;type:integer;default:500"`
	RankWeightsJSON           string    `gorm:"column:rank_weights_json;type:text;default:'{\"fts\":0.55,\"recency\":0.20,\"importance\":0.15,\"confidence\":0.10}'"`
	DefaultTopK               int       `gorm:"column:default_top_k;type:integer;default:10"`
	CreatedAt                 time.Time `gorm:"column:created_at;type:datetime;not null"`
	UpdatedAt                 time.Time `gorm:"column:updated_at;type:datetime;not null"`
}

// TableName specifies the table name for NamespacePolicy
func (NamespacePolicy) TableName() string {
	return "namespace_policies"
}

// MemoryEvent stores audit trail for all memory operations.
// Supports full traceability with actor, trace_id, and request_id.
type MemoryEvent struct {
	ID          string    `gorm:"column:id;type:text;primaryKey"`
	ItemID      *string   `gorm:"column:item_id;type:text;index:idx_event_item"`
	EventType   EventType `gorm:"column:event_type;type:text;not null;index:idx_event_type"`
	Actor       *string   `gorm:"column:actor;type:text;index:idx_event_actor"` // agent_id/user_id/system
	TraceID     *string   `gorm:"column:trace_id;type:text;index:idx_event_trace"`
	RequestID   *string   `gorm:"column:request_id;type:text;index:idx_event_request"`
	PayloadJSON *string   `gorm:"column:payload_json;type:text"`
	CreatedAt   time.Time `gorm:"column:created_at;type:datetime;not null;index:idx_event_created"`
}

// TableName specifies the table name for MemoryEvent
func (MemoryEvent) TableName() string {
	return "memory_events"
}

// DeletedItem stores soft-deleted memory items for recovery.
// Items are physically purged after purge_after date.
type DeletedItem struct {
	ID               string    `gorm:"column:id;type:text;primaryKey"`
	OriginalDataJSON string    `gorm:"column:original_data_json;type:text;not null"` // Full backup including FTS fields
	DeletedAt        time.Time `gorm:"column:deleted_at;type:datetime;not null;index:idx_del_deleted"`
	PurgeAfter       time.Time `gorm:"column:purge_after;type:datetime;not null;index:idx_del_purge"`
	DeletedBy        *string   `gorm:"column:deleted_by;type:text"`
	Reason           *string   `gorm:"column:reason;type:text"`
}

// TableName specifies the table name for DeletedItem
func (DeletedItem) TableName() string {
	return "deleted_items"
}

// FTSMemory represents the FTS5 virtual table for full-text search.
// The table and triggers are created by store.Migrate after AutoMigrate.
type FTSMemory struct {
	ItemID   string `gorm:"column:item_id"`
	Title    string `gorm:"column:title"`
	Content  string `gorm:"column:content"`
	Summary  string `gorm:"column:summary"`
	TagsText string `gorm:"column:tags_text"`
}

// TableName specifies the virtual table name
func (FTSMemory) TableName() string {
	return "fts_memory"
}

// IsVirtualTable indicates this is an FTS5 virtual table
func (FTSMemory) IsVirtualTable() bool {
	return true
}

// ==================== LLM Integration (v0.3) ====================

// LLMProvider defines the supported LLM providers
type LLMProvider string

const (
	LLMProviderOpenAI LLMProvider = "openai"
	LLMProviderClaude LLMProvider = "anthropic"
	LLMProviderOllama LLMProvider = "ollama"
	LLMProviderCustom LLMProvider = "custom"
)

// ExtractionStatus defines the status of dialog extraction
type ExtractionStatus string

const (
	ExtractionStatusPending    ExtractionStatus = "pending"
	ExtractionStatusProcessing ExtractionStatus = "processing"
	ExtractionStatusCompleted  ExtractionStatus = "completed"
	ExtractionStatusFailed     ExtractionStatus = "failed"
)

// LLMConfig stores LLM provider configuration.
// IMPORTANT: APIKey is stored as-is (plaintext). Encryption must be handled by the caller
// before storing or after retrieving. This library does NOT manage key encryption.
type LLMConfig struct {
	ID             string      `gorm:"column:id;type:text;primaryKey"`
	Name           string      `gorm:"column:name;type:text;not null"`
	Provider       LLMProvider `gorm:"column:provider;type:text;not null;index:idx_llm_provider"`
	APIKey         string      `gorm:"column:api_key;type:text"` // Plaintext - caller handles encryption/decryption
	BaseURL        *string     `gorm:"column:base_url;type:text"`
	Model          string      `gorm:"column:model;type:text;not null"`
	MaxTokens      int         `gorm:"column:max_tokens;type:integer;default:4096"`
	Temperature    float64     `gorm:"column:temperature;type:real;default:0.3"`
	TimeoutSeconds int         `gorm:"column:timeout_seconds;type:integer;default:30"`
	IsDefault      bool        `gorm:"column:is_default;type:boolean;default:0;uniqueIndex:idx_llm_default"`
	Enabled        bool        `gorm:"column:enabled;type:boolean;default:1"`
	CreatedAt      time.Time   `gorm:"column:created_at;type:datetime;not null"`
	UpdatedAt      time.Time   `gorm:"column:updated_at;type:datetime;not null"`
}

// TableName specifies the table name for LLMConfig
func (LLMConfig) TableName() string {
	return "llm_configs"
}

// BeforeCreate hook to ensure only one default config
// Note: Application layer should handle the actual logic of unsetting other defaults
func (c *LLMConfig) BeforeCreate(tx *gorm.DB) error {
	if c.ID == "" {
		c.ID = GenerateID()
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = time.Now()
	}
	return nil
}

// ExtractionPrompt stores prompt templates for memory extraction
type ExtractionPrompt struct {
	ID           string    `gorm:"column:id;type:text;primaryKey"`
	Name         string    `gorm:"column:name;type:text;not null;uniqueIndex"`
	Version      int       `gorm:"column:version;type:integer;default:1"`
	SystemPrompt string    `gorm:"column:system_prompt;type:text;not null"`
	JSONSchema   string    `gorm:"column:json_schema;type:text;not null"`
	IsDefault    bool      `gorm:"column:is_default;type:boolean;default:0;uniqueIndex:idx_prompt_default"`
	CreatedAt    time.Time `gorm:"column:created_at;type:datetime;not null"`
	UpdatedAt    time.Time `gorm:"column:updated_at;type:datetime;not null"`
}

// TableName specifies the table name for ExtractionPrompt
func (ExtractionPrompt) TableName() string {
	return "extraction_prompts"
}

// DialogExtraction records the extraction process and results
type DialogExtraction struct {
	ID                    string           `gorm:"column:id;type:text;primaryKey"`
	DialogText            string           `gorm:"column:dialog_text;type:text;not null"`
	DialogHash            string           `gorm:"column:dialog_hash;type:text;not null;uniqueIndex:idx_dialog_hash"` // SHA256 for idempotency
	LLMConfigID           string           `gorm:"column:llm_config_id;type:text;not null;index:idx_ext_llm"`
	PromptID              string           `gorm:"column:prompt_id;type:text;not null"`
	ExtractedMemoriesJSON string           `gorm:"column:extracted_memories_json;type:text"` // Array of ExtractedMemory
	TotalTokens           *int             `gorm:"column:total_tokens;type:integer"`
	CostEstimate          *float64         `gorm:"column:cost_estimate;type:real"`
	ProcessingTimeMs      *int             `gorm:"column:processing_time_ms;type:integer"`
	Status                ExtractionStatus `gorm:"column:status;type:text;not null;index:idx_ext_status"`
	ErrorMessage          *string          `gorm:"column:error_message;type:text"`
	CreatedAt             time.Time        `gorm:"column:created_at;type:datetime;not null;index:idx_ext_created"`
	CompletedAt           *time.Time       `gorm:"column:completed_at;type:datetime"`
}

// TableName specifies the table name for DialogExtraction
func (DialogExtraction) TableName() string {
	return "dialog_extractions"
}

// MemoryMerge tracks merge operations for memory consolidation
type MemoryMerge struct {
	ID             string    `gorm:"column:id;type:text;primaryKey"`
	TargetID       string    `gorm:"column:target_id;type:text;not null;index:idx_merge_target"` // The memory being updated
	SourceContent  string    `gorm:"column:source_content;type:text"`                            // Content being merged in
	SourceTitle    string    `gorm:"column:source_title;type:text"`
	MergedContent  string    `gorm:"column:merged_content;type:text"` // Result after merge
	MergedTitle    string    `gorm:"column:merged_title;type:text"`
	SourceDialogID string    `gorm:"column:source_dialog_id;type:text;index:idx_merge_dialog"` // Original extraction that triggered merge
	MergeType      string    `gorm:"column:merge_type;type:text;default:'auto'"`               // 'auto' (LLM) or 'manual'
	Reason         string    `gorm:"column:reason;type:text"`                                  // Why this merge happened
	CreatedAt      time.Time `gorm:"column:created_at;type:datetime;not null;index:idx_merge_created"`
}

// TableName specifies the table name for MemoryMerge
func (MemoryMerge) TableName() string {
	return "memory_merges"
}

// GenerateID generates a ULID (Universally Unique Lexicographically Sortable Identifier).
// Uses github.com/oklog/ulid for proper implementation.
func GenerateID() string {
	entropy := ulid.Monotonic(rand.Reader, 0)
	id, err := ulid.New(ulid.Timestamp(time.Now()), entropy)
	if err != nil {
		// Fallback to timestamp-based ID if ULID generation fails
		return fmt.Sprintf("%d%06d", time.Now().UnixMilli(), time.Now().Nanosecond()/1000)
	}
	return id.String()
}
