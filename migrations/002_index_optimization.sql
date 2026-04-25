-- Migration 002: Index Optimization for Common Query Patterns
-- These indexes improve performance for the most frequent queries

-- Composite index for Recall queries (namespace + status + expires_at)
-- This covers the common filter: namespace=X AND status='active' AND (expires_at IS NULL OR expires_at > now)
CREATE INDEX IF NOT EXISTS idx_mem_recall_filter ON memory_items(namespace, status, expires_at);

-- Composite index for namespace_type + created_at (for listing/sorting by time)
CREATE INDEX IF NOT EXISTS idx_mem_type_created ON memory_items(namespace_type, created_at);

-- Index for access_count queries (for sliding TTL threshold checks)
CREATE INDEX IF NOT EXISTS idx_mem_access ON memory_items(access_count);

-- Index for last_access_at (for sliding TTL renewal and analytics)
CREATE INDEX IF NOT EXISTS idx_mem_last_access ON memory_items(last_access_at);
