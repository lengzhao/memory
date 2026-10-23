package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
)

// Forgetting modes.
const (
	// ForgetSoft marks rows deleted: reversible, they disappear from reads and the search index.
	ForgetSoft = "soft"
	// ForgetExpire marks rows expired: same visibility effect, different bookkeeping meaning.
	ForgetExpire = "expire"
	// ForgetHard removes rows and their index entries. A recoverable copy is kept in
	// deleted_items unless BackupBeforeHardDelete is false.
	ForgetHard = "hard"
)

// DefaultDeletedRetention is how long a hard-deleted row stays recoverable in deleted_items
// before PurgeExpiredDeleted may drop it.
const DefaultDeletedRetention = 7 * 24 * time.Hour

// ForgetRequest removes memories.
//
// Inside a scope, NamespaceType selects one namespace; leaving it empty targets every namespace
// the scope can reach. Targeting the whole scope is the common case (a user asks to be forgotten),
// which is why an empty type is deliberately broad rather than narrow.
type ForgetRequest struct {
	ItemIDs       []string
	NamespaceType model.NamespaceType
	Mode          string
	Reason        string
	// BackupBeforeHardDelete keeps a copy of hard-deleted rows in deleted_items so the deletion
	// stays recoverable for DefaultDeletedRetention. Defaults to true; set it to false for an
	// immediate, unrecoverable erasure.
	BackupBeforeHardDelete *bool
}

// Forget removes or marks memories as deleted/expired and returns the number of affected rows.
// When specific ItemIDs are requested and nothing matches, it reports CodeNotFound instead of a
// silent zero.
func (s *MemoryService) Forget(ctx context.Context, scope Scope, req ForgetRequest) (int, error) {
	if err := scope.Validate(); err != nil {
		return 0, err
	}
	if req.NamespaceType != "" && !validNamespaceType(req.NamespaceType) {
		return 0, newErr(CodeValidation, fmt.Sprintf("invalid namespace_type %q", req.NamespaceType))
	}
	if req.Mode == "" {
		req.Mode = ForgetSoft
	}
	switch req.Mode {
	case ForgetSoft, ForgetExpire, ForgetHard:
	default:
		return 0, newErr(CodeValidation, fmt.Sprintf("invalid forget mode %q (want soft|expire|hard)", req.Mode))
	}

	namespaces := scope.Namespaces(namespaceTypeFilter(req.NamespaceType)...)
	scopeQuery := func(db *gorm.DB) *gorm.DB {
		query := db.WithContext(ctx).Model(&model.MemoryItem{}).Where("namespace IN ?", namespaces)
		if len(req.ItemIDs) > 0 {
			query = query.Where("id IN ?", req.ItemIDs)
		}
		return query
	}

	// Collect the affected IDs first: they drive callbacks and the search index cleanup (a status
	// change does not fire the DELETE trigger).
	var affected []string
	if err := scopeQuery(s.db).Where("status = ?", model.ItemStatusActive).Pluck("id", &affected).Error; err != nil {
		return 0, wrapErr(CodeInternal, "failed to resolve forget targets", err)
	}

	var rowsAffected int64
	switch req.Mode {
	case ForgetExpire:
		result := scopeQuery(s.db).Where("status = ?", model.ItemStatusActive).Update("status", model.ItemStatusExpired)
		if result.Error != nil {
			return 0, wrapErr(CodeInternal, "expire failed", result.Error)
		}
		rowsAffected = result.RowsAffected

	case ForgetHard:
		backup := req.BackupBeforeHardDelete == nil || *req.BackupBeforeHardDelete
		if err := s.hardDelete(ctx, scopeQuery, backup, req.Reason, &rowsAffected); err != nil {
			return 0, err
		}

	default: // ForgetSoft
		result := scopeQuery(s.db).Where("status = ?", model.ItemStatusActive).Update("status", model.ItemStatusDeleted)
		if result.Error != nil {
			return 0, wrapErr(CodeInternal, "soft delete failed", result.Error)
		}
		rowsAffected = result.RowsAffected
	}

	if req.Mode != ForgetHard && len(affected) > 0 {
		if err := pruneFTS(ctx, s.db, affected); err != nil {
			GetLogger().Warn("failed to prune search index after forget", "error", err, "item_count", len(affected))
		}
	}

	for _, id := range affected {
		if req.Mode == ForgetExpire {
			if s.config.OnExpired != nil {
				s.config.OnExpired(ctx, id)
			}
			continue
		}
		if s.config.OnDeleted != nil {
			s.config.OnDeleted(ctx, id)
		}
	}

	eventType := model.EventTypeDelete
	if req.Mode == ForgetExpire {
		eventType = model.EventTypeExpire
	}
	s.recordEvent(ctx, eventType, nil, strings.Join(namespaces, ","), map[string]interface{}{
		"mode":   req.Mode,
		"count":  int(rowsAffected),
		"reason": req.Reason,
	})

	if rowsAffected == 0 && len(req.ItemIDs) > 0 {
		return 0, wrapErr(CodeNotFound, "no memory matched the forget request", nil)
	}
	return int(rowsAffected), nil
}

// hardDelete removes rows and archives a recoverable copy when requested, in one transaction.
func (s *MemoryService) hardDelete(ctx context.Context, scopeQuery func(*gorm.DB) *gorm.DB, backup bool, reason string, rowsAffected *int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if backup {
			var items []model.MemoryItem
			if err := scopeQuery(tx).Find(&items).Error; err != nil {
				return wrapErr(CodeInternal, "failed to load items for backup", err)
			}
			now := time.Now()
			purgeAfter := now.Add(DefaultDeletedRetention)
			for _, item := range items {
				data, err := json.Marshal(item)
				if err != nil {
					return wrapErr(CodeInternal, "failed to serialise item for backup", err)
				}
				deleted := model.DeletedItem{
					ID:               item.ID,
					OriginalDataJSON: string(data),
					DeletedAt:        now,
					PurgeAfter:       purgeAfter,
					Reason:           optionalString(reason),
				}
				if err := tx.Create(&deleted).Error; err != nil {
					return wrapErr(CodeInternal, "failed to archive item before hard delete", err)
				}
			}
		}

		result := scopeQuery(tx).Delete(&model.MemoryItem{})
		if result.Error != nil {
			return wrapErr(CodeInternal, "hard delete failed", result.Error)
		}
		*rowsAffected = result.RowsAffected
		return nil
	})
}

// UpdateRequest mutates one memory. ExpectedVersion is required: every update is an optimistic
// CAS, so a concurrent writer cannot be silently overwritten.
type UpdateRequest struct {
	ItemID          string
	Title           *string
	Content         *string
	Summary         *string
	Tags            []string
	Importance      *int
	Confidence      *float64
	Ephemeral       *bool
	ExpectedVersion int
}

// Update applies a versioned update. The row must belong to the scope, and the ownership check
// runs inside the same statement as the version compare, so an out-of-scope ID is reported as
// not-found rather than updated.
func (s *MemoryService) Update(ctx context.Context, scope Scope, req UpdateRequest) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if req.ExpectedVersion <= 0 {
		return newErr(CodeValidation, "expected_version must be greater than 0")
	}
	if req.Importance != nil && (*req.Importance < 0 || *req.Importance > 100) {
		return newErr(CodeValidation, fmt.Sprintf("importance must be within 0-100, got %d", *req.Importance))
	}
	if req.Confidence != nil && (*req.Confidence < 0 || *req.Confidence > 1) {
		return newErr(CodeValidation, fmt.Sprintf("confidence must be within 0.0-1.0, got %v", *req.Confidence))
	}
	if req.Content != nil {
		if strings.TrimSpace(*req.Content) == "" {
			return newErr(CodeValidation, "content must not be empty")
		}
		if len([]rune(*req.Content)) > MaxContentRunes {
			return newErr(CodeValidation, fmt.Sprintf("content exceeds %d runes", MaxContentRunes))
		}
	}

	scoped := func(db *gorm.DB) *gorm.DB {
		return db.WithContext(ctx).Model(&model.MemoryItem{}).
			Where("id = ?", req.ItemID).
			Where("namespace IN ?", scope.Namespaces())
	}

	var item model.MemoryItem
	if err := scoped(s.db).First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return wrapErr(CodeNotFound, "memory not found in this scope", err)
		}
		return wrapErr(CodeInternal, "failed to load memory", err)
	}

	updates := map[string]interface{}{
		"version":    gorm.Expr("version + 1"),
		"updated_at": time.Now(),
	}
	if req.Title != nil {
		updates["title"] = *req.Title
	}
	if req.Content != nil {
		updates["content"] = *req.Content
	}
	if req.Summary != nil {
		updates["summary"] = *req.Summary
	}
	if len(req.Tags) > 0 {
		tagsJSON, _ := json.Marshal(req.Tags)
		updates["tags_json"] = string(tagsJSON)
	}
	if req.Importance != nil {
		updates["importance"] = *req.Importance
	}
	if req.Confidence != nil {
		updates["confidence"] = *req.Confidence
	}
	if req.Title != nil || req.Content != nil || req.Summary != nil {
		title, content, summary := item.Title, item.Content, item.Summary
		if req.Title != nil {
			title = *req.Title
		}
		if req.Content != nil {
			content = *req.Content
		}
		if req.Summary != nil {
			summary = *req.Summary
		}
		updates["tokenized_text"] = TokenizeForSearch(title + " " + content + " " + summary)
	}

	// A fresh statement: the scoped read above must not leak its predicates into the write.
	rowsAffected, err := updateRows(ctx, scoped(s.db).Where("version = ?", req.ExpectedVersion), updates)
	if err != nil {
		return wrapErr(CodeInternal, "update failed", err)
	}
	if rowsAffected == 0 {
		var current model.MemoryItem
		if err := s.db.WithContext(ctx).Select("id, version").First(&current, "id = ?", req.ItemID).Error; err != nil {
			return wrapErr(CodeNotFound, "memory not found", err)
		}
		return wrapErr(CodeConflict, fmt.Sprintf("version conflict: expected %d, got %d", req.ExpectedVersion, current.Version), nil)
	}

	s.recordEvent(ctx, model.EventTypeUpdate, &req.ItemID, item.Namespace, nil)
	if s.config.OnUpdated != nil {
		var updated model.MemoryItem
		if err := s.db.WithContext(ctx).First(&updated, "id = ?", req.ItemID).Error; err == nil {
			s.config.OnUpdated(ctx, updated)
		}
	}
	return nil
}

// Touch records access. It backs sliding TTL and usage-based ranking.
func (s *MemoryService) Touch(ctx context.Context, scope Scope, itemID string) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("id = ?", itemID).
		Where("namespace IN ?", scope.Namespaces()).
		Updates(map[string]interface{}{
			"access_count":   gorm.Expr("access_count + 1"),
			"last_access_at": time.Now(),
		})
	if result.Error != nil {
		return wrapErr(CodeInternal, "touch failed", result.Error)
	}
	if result.RowsAffected == 0 {
		return wrapErr(CodeNotFound, fmt.Sprintf("memory not found in this scope: %s", itemID), nil)
	}
	return nil
}

// TouchWithRenew touches a memory and, for sliding-TTL rows, renews its expiry every threshold
// accesses. It reports whether the expiry was renewed.
func (s *MemoryService) TouchWithRenew(ctx context.Context, scope Scope, itemID string, threshold, ttlSeconds int) (bool, error) {
	if err := scope.Validate(); err != nil {
		return false, err
	}
	var item model.MemoryItem
	if err := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("id = ?", itemID).Where("namespace IN ?", scope.Namespaces()).
		First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, wrapErr(CodeNotFound, "memory not found in this scope", err)
		}
		return false, wrapErr(CodeInternal, "failed to load memory", err)
	}

	if item.ExpiresAt == nil || threshold <= 0 || (item.AccessCount+1)%threshold != 0 {
		return false, s.Touch(ctx, scope, itemID)
	}

	result := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("id = ?", itemID).
		Where("namespace IN ?", scope.Namespaces()).
		Updates(map[string]interface{}{
			"access_count":   gorm.Expr("access_count + 1"),
			"last_access_at": time.Now(),
			"expires_at":     time.Now().Add(time.Duration(ttlSeconds) * time.Second),
		})
	if result.Error != nil {
		return false, wrapErr(CodeInternal, "renew failed", result.Error)
	}
	return true, nil
}

// RenewExpiration sets a new expiry for one memory.
func (s *MemoryService) RenewExpiration(ctx context.Context, scope Scope, itemID string, ttlSeconds int) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("id = ?", itemID).
		Where("namespace IN ?", scope.Namespaces()).
		Update("expires_at", time.Now().Add(time.Duration(ttlSeconds)*time.Second))
	if result.Error != nil {
		return wrapErr(CodeInternal, "renew expiration failed", result.Error)
	}
	if result.RowsAffected == 0 {
		return wrapErr(CodeNotFound, fmt.Sprintf("memory not found in this scope: %s", itemID), nil)
	}
	return nil
}

// CleanupExpired marks expired rows and removes them from the search index, across every scope.
// Expiry is a property of the row, not of a caller, so this is maintenance work with no scope
// argument: it is normally driven by the Janitor.
func (s *MemoryService) CleanupExpired(ctx context.Context) (int64, error) {
	var itemIDs []string
	if err := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("status = ? AND expires_at IS NOT NULL AND expires_at < ?", model.ItemStatusActive, time.Now()).
		Pluck("id", &itemIDs).Error; err != nil {
		return 0, wrapErr(CodeInternal, "failed to resolve expired memories", err)
	}
	if len(itemIDs) == 0 {
		return 0, nil
	}

	result := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("id IN ? AND status = ?", itemIDs, model.ItemStatusActive).
		Update("status", model.ItemStatusExpired)
	if result.Error != nil {
		return 0, wrapErr(CodeInternal, "cleanup failed", result.Error)
	}
	if err := pruneFTS(ctx, s.db, itemIDs); err != nil {
		GetLogger().Warn("failed to prune search index after cleanup", "error", err, "item_count", len(itemIDs))
	}
	for _, id := range itemIDs {
		if s.config.OnExpired != nil {
			s.config.OnExpired(ctx, id)
		}
	}
	return result.RowsAffected, nil
}

// PurgeDeleted removes soft-deleted rows older than before from memory_items, archiving them in
// deleted_items (recoverable until DefaultDeletedRetention elapses).
func (s *MemoryService) PurgeDeleted(ctx context.Context, before time.Time) (int64, error) {
	var purged int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var items []model.MemoryItem
		if err := tx.Where("status = ? AND updated_at < ?", model.ItemStatusDeleted, before).
			Find(&items).Error; err != nil {
			return wrapErr(CodeInternal, "failed to load purge candidates", err)
		}
		if len(items) == 0 {
			return nil
		}

		now := time.Now()
		purgeAfter := now.Add(DefaultDeletedRetention)
		for _, item := range items {
			data, err := json.Marshal(item)
			if err != nil {
				return wrapErr(CodeInternal, "failed to serialise item for archive", err)
			}
			if err := tx.Create(&model.DeletedItem{
				ID:               item.ID,
				OriginalDataJSON: string(data),
				DeletedAt:        now,
				PurgeAfter:       purgeAfter,
			}).Error; err != nil {
				return wrapErr(CodeInternal, "failed to archive purged item", err)
			}
		}

		result := tx.Where("status = ? AND updated_at < ?", model.ItemStatusDeleted, before).
			Delete(&model.MemoryItem{})
		if result.Error != nil {
			return wrapErr(CodeInternal, "purge failed", result.Error)
		}
		purged = result.RowsAffected
		return nil
	})
	if err != nil {
		return 0, err
	}
	return purged, nil
}

// PurgeExpiredDeleted irreversibly drops archived rows whose retention window has passed. This is
// the step that finally removes the retained copy of a hard delete:
// Forget(mode=hard) -> PurgeExpiredDeleted.
func (s *MemoryService) PurgeExpiredDeleted(ctx context.Context, now time.Time) (int64, error) {
	result := s.db.WithContext(ctx).Where("purge_after <= ?", now).Delete(&model.DeletedItem{})
	if result.Error != nil {
		return 0, wrapErr(CodeInternal, "purge of archived rows failed", result.Error)
	}
	return result.RowsAffected, nil
}

// PurgeDeletedNow drops the archived copy of specific items immediately, ignoring the retention
// window. Use it when a subject demands immediate erasure.
func (s *MemoryService) PurgeDeletedNow(ctx context.Context, itemIDs []string) (int64, error) {
	if len(itemIDs) == 0 {
		return 0, newErr(CodeValidation, "item_ids must not be empty")
	}
	result := s.db.WithContext(ctx).Where("id IN ?", itemIDs).Delete(&model.DeletedItem{})
	if result.Error != nil {
		return 0, wrapErr(CodeInternal, "purge of archived rows failed", result.Error)
	}
	return result.RowsAffected, nil
}

// RebuildFTS rebuilds the search index from the active rows. It is an operational repair tool;
// Migrate already reconciles the index, so it is not part of the normal flow.
func (s *MemoryService) RebuildFTS(ctx context.Context) error {
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM fts_memory").Error; err != nil {
			return wrapErr(CodeInternal, "failed to clear search index", err)
		}
		var items []model.MemoryItem
		if err := tx.Where("status = ?", model.ItemStatusActive).Find(&items).Error; err != nil {
			return wrapErr(CodeInternal, "failed to load rows for reindex", err)
		}
		for _, item := range items {
			if err := tx.Exec("INSERT INTO fts_memory (item_id, tokenized_content) VALUES (?, ?)",
				item.ID, item.TokenizedText).Error; err != nil {
				return wrapErr(CodeInternal, "failed to reindex row "+item.ID, err)
			}
		}
		return nil
	}); err != nil {
		return err
	}

	ok, err := s.ValidateFTS(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return wrapErr(CodeInternal, "reindex completed but the index still disagrees with memory_items", nil)
	}
	return nil
}

// ValidateFTS reports whether the search index holds exactly one row per active memory.
// Migrate and every write path maintain this invariant, so a false result means corruption or an
// interrupted maintenance job.
func (s *MemoryService) ValidateFTS(ctx context.Context) (bool, error) {
	var activeCount int64
	if err := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("status = ?", model.ItemStatusActive).Count(&activeCount).Error; err != nil {
		return false, wrapErr(CodeInternal, "failed to count active memories", err)
	}
	var indexedCount int64
	if err := s.db.WithContext(ctx).Raw("SELECT COUNT(*) FROM fts_memory").Scan(&indexedCount).Error; err != nil {
		return false, wrapErr(CodeInternal, "failed to count indexed rows", err)
	}
	return activeCount == indexedCount, nil
}

// pruneFTS removes rows from the search index. Status changes are not covered by the DELETE
// trigger, so every status transition out of "active" must call this. The status triggers in
// store/fts.go keep the index correct even if a caller forgets.
func pruneFTS(ctx context.Context, db *gorm.DB, itemIDs []string) error {
	if len(itemIDs) == 0 {
		return nil
	}
	return db.WithContext(ctx).Exec("DELETE FROM fts_memory WHERE item_id IN ?", itemIDs).Error
}

// namespaceTypeFilter turns an optional namespace type into the variadic argument of
// Scope.Namespaces.
func namespaceTypeFilter(nsType model.NamespaceType) []model.NamespaceType {
	if nsType == "" {
		return nil
	}
	return []model.NamespaceType{nsType}
}

func optionalString(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
