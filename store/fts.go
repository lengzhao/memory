// Package store: FTS5 setup used by Migrate (GORM does not support FTS5 virtual tables).
package store

import (
	"fmt"

	"gorm.io/gorm"
)

// installFTS5 creates the fts_memory virtual table, sync triggers, and backfills from memory_items.
// Safe to call repeatedly; uses IF NOT EXISTS and idempotent backfill.
func installFTS5(db *gorm.DB) error {
	statements := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS fts_memory USING fts5(
			title,
			content,
			summary,
			tags_text,
			item_id UNINDEXED,
			tokenize='porter unicode61'
		)`,
		`CREATE TRIGGER IF NOT EXISTS trg_fts_insert AFTER INSERT ON memory_items
BEGIN
	INSERT INTO fts_memory (item_id, title, content, summary, tags_text)
	VALUES (
		NEW.id,
		COALESCE(NEW.title, ''),
		NEW.content,
		COALESCE(NEW.summary, ''),
		COALESCE(NEW.tags_json, '')
	);
END`,
		`CREATE TRIGGER IF NOT EXISTS trg_fts_update AFTER UPDATE ON memory_items
BEGIN
	UPDATE fts_memory SET
		title = COALESCE(NEW.title, ''),
		content = NEW.content,
		summary = COALESCE(NEW.summary, ''),
		tags_text = COALESCE(NEW.tags_json, '')
	WHERE item_id = NEW.id;
END`,
		`CREATE TRIGGER IF NOT EXISTS trg_fts_delete AFTER DELETE ON memory_items
BEGIN
	DELETE FROM fts_memory WHERE item_id = OLD.id;
END`,
	}

	for _, q := range statements {
		if err := db.Exec(q).Error; err != nil {
			return fmt.Errorf("fts5 setup: %w", err)
		}
	}

	// Index existing rows (e.g. first run on DB that only had GORM tables)
	return db.Exec(`
		INSERT INTO fts_memory (item_id, title, content, summary, tags_text)
		SELECT
			m.id,
			COALESCE(m.title, ''),
			m.content,
			COALESCE(m.summary, ''),
			COALESCE(m.tags_json, '')
		FROM memory_items m
		WHERE NOT EXISTS (SELECT 1 FROM fts_memory f WHERE f.item_id = m.id)
	`).Error
}
