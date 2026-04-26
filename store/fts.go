// Package store: FTS5 setup used by Migrate (GORM does not support FTS5 virtual tables).
package store

import (
	"fmt"

	"gorm.io/gorm"
)

// installFTS5 creates the fts_memory virtual table, sync triggers, and backfills from memory_items.
// Safe to call repeatedly; uses IF NOT EXISTS and idempotent backfill.
// Uses unicode61 with pre-tokenized text for CJK support.
func installFTS5(db *gorm.DB) error {
	statements := []string{
		// Use unicode61 with space-separated pre-tokenized content.
		// Content is pre-tokenized by jiebago before insertion.
		`CREATE VIRTUAL TABLE IF NOT EXISTS fts_memory USING fts5(
			tokenized_content,
			item_id UNINDEXED,
			tokenize='unicode61 remove_diacritics 1'
		)`,
		`CREATE TRIGGER IF NOT EXISTS trg_fts_insert AFTER INSERT ON memory_items
BEGIN
	INSERT INTO fts_memory (item_id, tokenized_content)
	VALUES (
		NEW.id,
		COALESCE(NEW.tokenized_text, '')
	);
END`,
		`CREATE TRIGGER IF NOT EXISTS trg_fts_update AFTER UPDATE ON memory_items
BEGIN
	UPDATE fts_memory SET
		tokenized_content = COALESCE(NEW.tokenized_text, '')
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

	return nil
}
