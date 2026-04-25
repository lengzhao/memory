-- Migration 001: Initial Schema
-- Creates all tables, indexes, FTS5 virtual table, and triggers
-- Compatible with SQLite 3.35+ with FTS5 extension

-- Memory Items Table
CREATE TABLE IF NOT EXISTS memory_items (
    id TEXT PRIMARY KEY,
    namespace TEXT NOT NULL,
    namespace_type TEXT NOT NULL,
    title TEXT,
    content TEXT NOT NULL,
    summary TEXT,
    tags_json TEXT,
    source_type TEXT,
    source_ref TEXT,
    importance INTEGER DEFAULT 0,
    confidence REAL DEFAULT 1.0,
    status TEXT DEFAULT 'active',
    expires_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    last_access_at DATETIME,
    access_count INTEGER DEFAULT 0,
    version INTEGER DEFAULT 1,
    dedupe_key TEXT,
    embedding_ref TEXT,
    UNIQUE(namespace, dedupe_key)
);

-- Memory Items Indexes
CREATE INDEX IF NOT EXISTS idx_mem_namespace ON memory_items(namespace);
CREATE INDEX IF NOT EXISTS idx_mem_ns_type ON memory_items(namespace_type);
CREATE INDEX IF NOT EXISTS idx_mem_status ON memory_items(status);
CREATE INDEX IF NOT EXISTS idx_mem_expires ON memory_items(expires_at);
CREATE INDEX IF NOT EXISTS idx_mem_created ON memory_items(created_at);

-- Memory Links Table
CREATE TABLE IF NOT EXISTS memory_links (
    id TEXT PRIMARY KEY,
    from_id TEXT NOT NULL,
    to_id TEXT NOT NULL,
    link_type TEXT NOT NULL,
    weight REAL DEFAULT 1.0,
    created_at DATETIME NOT NULL,
    reason_json TEXT
);

-- Memory Links Indexes
CREATE INDEX IF NOT EXISTS idx_link_from ON memory_links(from_id);
CREATE INDEX IF NOT EXISTS idx_link_to ON memory_links(to_id);
CREATE INDEX IF NOT EXISTS idx_link_type ON memory_links(link_type);

-- Namespace Summaries Table
CREATE TABLE IF NOT EXISTS namespace_summaries (
    id TEXT PRIMARY KEY,
    namespace TEXT UNIQUE NOT NULL,
    summary TEXT NOT NULL,
    item_count INTEGER NOT NULL,
    window_start DATETIME,
    window_end DATETIME,
    updated_at DATETIME NOT NULL
);

-- Namespace Policies Table
CREATE TABLE IF NOT EXISTS namespace_policies (
    namespace TEXT PRIMARY KEY,
    ttl_seconds INTEGER,
    ttl_policy TEXT DEFAULT 'fixed',
    sliding_ttl_threshold INTEGER DEFAULT 3,
    summary_enabled INTEGER DEFAULT 1,
    summary_item_token_threshold INTEGER DEFAULT 500,
    rank_weights_json TEXT DEFAULT '{"fts":0.55,"recency":0.20,"importance":0.15,"confidence":0.10}',
    default_top_k INTEGER DEFAULT 10,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

-- Memory Events Table
CREATE TABLE IF NOT EXISTS memory_events (
    id TEXT PRIMARY KEY,
    item_id TEXT,
    event_type TEXT NOT NULL,
    actor TEXT,
    trace_id TEXT,
    request_id TEXT,
    payload_json TEXT,
    created_at DATETIME NOT NULL
);

-- Memory Events Indexes
CREATE INDEX IF NOT EXISTS idx_event_item ON memory_events(item_id);
CREATE INDEX IF NOT EXISTS idx_event_type ON memory_events(event_type);
CREATE INDEX IF NOT EXISTS idx_event_actor ON memory_events(actor);
CREATE INDEX IF NOT EXISTS idx_event_trace ON memory_events(trace_id);
CREATE INDEX IF NOT EXISTS idx_event_request ON memory_events(request_id);
CREATE INDEX IF NOT EXISTS idx_event_created ON memory_events(created_at);

-- Deleted Items Table (Soft Delete with Recovery)
CREATE TABLE IF NOT EXISTS deleted_items (
    id TEXT PRIMARY KEY,
    original_data_json TEXT NOT NULL,
    deleted_at DATETIME NOT NULL,
    purge_after DATETIME NOT NULL,
    deleted_by TEXT,
    reason TEXT
);

-- Deleted Items Indexes
CREATE INDEX IF NOT EXISTS idx_del_deleted ON deleted_items(deleted_at);
CREATE INDEX IF NOT EXISTS idx_del_purge ON deleted_items(purge_after);

-- FTS5 Virtual Table for Full-Text Search
-- Note: UNINDEXED means item_id is stored but not tokenized/searchable
CREATE VIRTUAL TABLE IF NOT EXISTS fts_memory USING fts5(
    title,
    content,
    summary,
    tags_text,
    item_id UNINDEXED,
    tokenize='porter unicode61'
);

-- Trigger: Insert into FTS when memory item is created
CREATE TRIGGER IF NOT EXISTS trg_fts_insert AFTER INSERT ON memory_items
BEGIN
    INSERT INTO fts_memory (item_id, title, content, summary, tags_text)
    VALUES (
        NEW.id,
        COALESCE(NEW.title, ''),
        NEW.content,
        COALESCE(NEW.summary, ''),
        COALESCE(NEW.tags_json, '')
    );
END;

-- Trigger: Update FTS when memory item is updated
CREATE TRIGGER IF NOT EXISTS trg_fts_update AFTER UPDATE ON memory_items
BEGIN
    UPDATE fts_memory SET
        title = COALESCE(NEW.title, ''),
        content = NEW.content,
        summary = COALESCE(NEW.summary, ''),
        tags_text = COALESCE(NEW.tags_json, '')
    WHERE item_id = NEW.id;
END;

-- Trigger: Delete from FTS when memory item is deleted
CREATE TRIGGER IF NOT EXISTS trg_fts_delete AFTER DELETE ON memory_items
BEGIN
    DELETE FROM fts_memory WHERE item_id = OLD.id;
END;

-- Trigger: Update updated_at timestamp on memory_items
CREATE TRIGGER IF NOT EXISTS trg_mem_updated_at
AFTER UPDATE ON memory_items
FOR EACH ROW
BEGIN
    UPDATE memory_items SET updated_at = CURRENT_TIMESTAMP
    WHERE id = NEW.id AND updated_at = NEW.updated_at;
END;

-- Trigger: Update updated_at timestamp on namespace_policies
CREATE TRIGGER IF NOT EXISTS trg_policy_updated_at
AFTER UPDATE ON namespace_policies
FOR EACH ROW
BEGIN
    UPDATE namespace_policies SET updated_at = CURRENT_TIMESTAMP
    WHERE namespace = NEW.namespace AND updated_at = NEW.updated_at;
END;

-- ==================== LLM Integration (v0.3) ====================

-- LLM Configs Table
CREATE TABLE IF NOT EXISTS llm_configs (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    provider TEXT NOT NULL, -- openai/anthropic/ollama/custom
    api_key TEXT, -- Encrypted at application layer
    base_url TEXT,
    model TEXT NOT NULL,
    max_tokens INTEGER DEFAULT 4096,
    temperature REAL DEFAULT 0.3,
    timeout_seconds INTEGER DEFAULT 30,
    is_default INTEGER DEFAULT 0,
    enabled INTEGER DEFAULT 1,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

-- LLM Configs Indexes
CREATE INDEX IF NOT EXISTS idx_llm_provider ON llm_configs(provider);
CREATE UNIQUE INDEX IF NOT EXISTS idx_llm_default ON llm_configs(is_default) WHERE is_default = 1;

-- Extraction Prompts Table
CREATE TABLE IF NOT EXISTS extraction_prompts (
    id TEXT PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,
    version INTEGER DEFAULT 1,
    system_prompt TEXT NOT NULL,
    json_schema TEXT NOT NULL,
    is_default INTEGER DEFAULT 0,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

-- Extraction Prompts Indexes
CREATE UNIQUE INDEX IF NOT EXISTS idx_prompt_default ON extraction_prompts(is_default) WHERE is_default = 1;

-- Dialog Extractions Table
CREATE TABLE IF NOT EXISTS dialog_extractions (
    id TEXT PRIMARY KEY,
    dialog_text TEXT NOT NULL,
    dialog_hash TEXT NOT NULL, -- SHA256 for idempotency
    llm_config_id TEXT NOT NULL,
    prompt_id TEXT NOT NULL,
    extracted_memories_json TEXT,
    total_tokens INTEGER,
    cost_estimate REAL,
    processing_time_ms INTEGER,
    status TEXT NOT NULL, -- pending/processing/completed/failed
    error_message TEXT,
    created_at DATETIME NOT NULL,
    completed_at DATETIME
);

-- Dialog Extractions Indexes
CREATE UNIQUE INDEX IF NOT EXISTS idx_dialog_hash ON dialog_extractions(dialog_hash);
CREATE INDEX IF NOT EXISTS idx_ext_llm ON dialog_extractions(llm_config_id);
CREATE INDEX IF NOT EXISTS idx_ext_status ON dialog_extractions(status);
CREATE INDEX IF NOT EXISTS idx_ext_created ON dialog_extractions(created_at);

-- Triggers for LLM tables
CREATE TRIGGER IF NOT EXISTS trg_llm_updated_at
AFTER UPDATE ON llm_configs
FOR EACH ROW
BEGIN
    UPDATE llm_configs SET updated_at = CURRENT_TIMESTAMP
    WHERE id = NEW.id AND updated_at = NEW.updated_at;
END;

CREATE TRIGGER IF NOT EXISTS trg_prompt_updated_at
AFTER UPDATE ON extraction_prompts
FOR EACH ROW
BEGIN
    UPDATE extraction_prompts SET updated_at = CURRENT_TIMESTAMP
    WHERE id = NEW.id AND updated_at = NEW.updated_at;
END;
