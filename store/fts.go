// Package store: FTS5 setup used by Migrate (GORM does not support FTS5 virtual tables).
package store

import (
	"fmt"

	"gorm.io/gorm"
)

// installFTS5 creates the fts_memory virtual table, the sync triggers, and reconciles
// the index with the current active rows. Safe to call repeatedly; every statement is
// idempotent.
//
// Index membership invariant: fts_memory holds exactly one row per memory_items row
// whose status is 'active'. Soft deletes (status -> expired/archived/deleted) remove the
// row from the index, so search cannot return stale items and ValidateFTS stays meaningful.
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
WHEN NEW.status = 'active'
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
		// Status transitions control index membership.
		// On entering 'active' (e.g. restore) the row is (re-)indexed.
		`CREATE TRIGGER IF NOT EXISTS trg_fts_status_active AFTER UPDATE OF status ON memory_items
WHEN NEW.status = 'active' AND OLD.status <> 'active'
BEGIN
	DELETE FROM fts_memory WHERE item_id = NEW.id;
	INSERT INTO fts_memory (item_id, tokenized_content)
	VALUES (NEW.id, COALESCE(NEW.tokenized_text, ''));
END`,
		// On leaving 'active' (expire / archive / soft / decision-engine delete) the row
		// must leave the index. Without this, deleted memories stay searchable forever.
		`CREATE TRIGGER IF NOT EXISTS trg_fts_status_inactive AFTER UPDATE OF status ON memory_items
WHEN NEW.status <> 'active'
BEGIN
	DELETE FROM fts_memory WHERE item_id = NEW.id;
END`,
		// Reconciliation for databases that were populated before the triggers existed
		// (or after an interrupted rebuild): index missing active rows, drop orphans.
		`INSERT INTO fts_memory (item_id, tokenized_content)
SELECT m.id, COALESCE(m.tokenized_text, '')
FROM memory_items m
WHERE m.status = 'active'
  AND m.id NOT IN (SELECT item_id FROM fts_memory WHERE item_id IS NOT NULL)`,
		`DELETE FROM fts_memory
WHERE item_id IS NOT NULL
  AND item_id NOT IN (SELECT id FROM memory_items WHERE status = 'active')`,
	}

	for _, q := range statements {
		if err := db.Exec(q).Error; err != nil {
			return fmt.Errorf("fts5 setup: %w", err)
		}
	}

	return nil
}
