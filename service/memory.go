// Package service provides the core memory service implementation.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
	memerrors "github.com/lengzhao/memory/pkg/errors"
)

// Config holds configuration and callbacks for MemoryService.
type Config struct {
	// Lifecycle callbacks (all optional)
	OnCreated func(ctx context.Context, item model.MemoryItem)
	OnUpdated func(ctx context.Context, item model.MemoryItem)
	OnDeleted func(ctx context.Context, itemID string)
	OnExpired func(ctx context.Context, itemID string)
}

// MemoryService provides core memory operations.
type MemoryService struct {
	db     *gorm.DB
	config Config
}

// NewMemoryService creates a new memory service instance.
func NewMemoryService(db *gorm.DB) *MemoryService {
	return &MemoryService{db: db}
}

// NewMemoryServiceWithConfig creates a new memory service with config.
func NewMemoryServiceWithConfig(db *gorm.DB, config Config) *MemoryService {
	return &MemoryService{db: db, config: config}
}

// RememberRequest represents a request to store a memory.
type RememberRequest struct {
	Namespace       string
	NamespaceType model.NamespaceType
	Title         string
	Content       string
	Summary       string
	Tags          []string
	SourceType    model.SourceType
	SourceRef     string
	Importance    int
	Confidence    float64
	TTLSeconds    *int
	DedupeKey     *string
}

// Remember stores a memory item with idempotency support.
func (s *MemoryService) Remember(ctx context.Context, req RememberRequest) (string, error) {
	now := time.Now()

	// Check for duplicate by dedupe_key if provided
	if req.DedupeKey != nil && *req.DedupeKey != "" {
		var existing model.MemoryItem
		err := s.db.WithContext(ctx).
			Where("namespace = ? AND dedupe_key = ?", req.Namespace, *req.DedupeKey).
			First(&existing).Error
		if err == nil {
			// Duplicate found, return existing ID
			return existing.ID, nil
		}
	}

	// Calculate expires_at if TTL provided
	var expiresAt *time.Time
	if req.TTLSeconds != nil && *req.TTLSeconds > 0 {
		t := now.Add(time.Duration(*req.TTLSeconds) * time.Second)
		expiresAt = &t
	}

	// Create new item
	tagsJSON, _ := json.Marshal(req.Tags)
	item := model.MemoryItem{
		ID:            model.GenerateID(),
		Namespace:     req.Namespace,
		NamespaceType: req.NamespaceType,
		Title:         req.Title,
		Content:       req.Content,
		Summary:       req.Summary,
		TagsJSON:      string(tagsJSON),
		SourceType:    req.SourceType,
		SourceRef:     req.SourceRef,
		Importance:    req.Importance,
		Confidence:    req.Confidence,
		Status:        model.ItemStatusActive,
		ExpiresAt:     expiresAt,
		CreatedAt:     now,
		UpdatedAt:     now,
		Version:       1,
		DedupeKey:     req.DedupeKey,
	}

	if err := s.db.WithContext(ctx).Create(&item).Error; err != nil {
		return "", memerrors.Wrap(memerrors.CodeInternal, "failed to create memory", err)
	}

	// Trigger callback
	if s.config.OnCreated != nil {
		s.config.OnCreated(ctx, item)
	}

	return item.ID, nil
}

// RecallRequest represents a request to recall memories.
type RecallRequest struct {
	Query            string
	Namespaces       []string
	NamespaceTypes   []model.NamespaceType
	TagsAny          []string
	TagsAll          []string
	TimeRangeStart   *time.Time
	TimeRangeEnd     *time.Time
	IncludeExpired   bool
	MinConfidence    float64
	MinImportance    int
	TopK             int
	ExcludeItemIDs   []string
}

// MemoryHit represents a recalled memory with relevance info.
type MemoryHit struct {
	model.MemoryItem
	Score          float64
	FTSScore       float64
	RecencyScore   float64
	ImportanceScore float64
	ConfidenceScore float64
	MatchReasons   []string
}

// Recall searches for memories using FTS and filtering.
func (s *MemoryService) Recall(ctx context.Context, req RecallRequest) ([]MemoryHit, error) {
	if req.TopK <= 0 {
		req.TopK = 10
	}
	if req.MinConfidence <= 0 {
		req.MinConfidence = 0.5
	}

	// Build base query
	query := s.db.WithContext(ctx).Model(&model.MemoryItem{})

	// Status filter
	query = query.Where("status = ?", model.ItemStatusActive)

	// Expiry filter (unless include expired)
	if !req.IncludeExpired {
		query = query.Where("expires_at IS NULL OR expires_at > ?", time.Now())
	}

	// Namespace filters
	if len(req.Namespaces) > 0 {
		query = query.Where("namespace IN ?", req.Namespaces)
	}
	if len(req.NamespaceTypes) > 0 {
		query = query.Where("namespace_type IN ?", req.NamespaceTypes)
	}

	// Tag filters (using JSON contains)
	if len(req.TagsAll) > 0 {
		for _, tag := range req.TagsAll {
			query = query.Where("tags_json LIKE ?", fmt.Sprintf("%%\"%s\"%%", tag))
		}
	}
	if len(req.TagsAny) > 0 {
		conditions := ""
		params := []interface{}{}
		for i, tag := range req.TagsAny {
			if i > 0 {
				conditions += " OR "
			}
			conditions += "tags_json LIKE ?"
			params = append(params, fmt.Sprintf("%%\"%s\"%%", tag))
		}
		query = query.Where("("+conditions+")", params...)
	}

	// Time range
	if req.TimeRangeStart != nil {
		query = query.Where("created_at >= ?", *req.TimeRangeStart)
	}
	if req.TimeRangeEnd != nil {
		query = query.Where("created_at <= ?", *req.TimeRangeEnd)
	}

	// Exclude specific items
	if len(req.ExcludeItemIDs) > 0 {
		query = query.Where("id NOT IN ?", req.ExcludeItemIDs)
	}

	// FTS search if query provided
	var itemIDs []string
	if req.Query != "" {
		// Use FTS5 for text search
		var ftsResults []struct {
			ItemID string
		}
		err := s.db.WithContext(ctx).Raw(`
			SELECT item_id FROM fts_memory 
			WHERE fts_memory MATCH ? 
			LIMIT ?
		`, req.Query, req.TopK*3).Scan(&ftsResults).Error
		if err != nil {
			return nil, memerrors.Wrap(memerrors.CodeInternal, "fts search failed", err)
		}
		for _, r := range ftsResults {
			itemIDs = append(itemIDs, r.ItemID)
		}
		if len(itemIDs) == 0 {
			return []MemoryHit{}, nil
		}
		query = query.Where("id IN ?", itemIDs)
	}

	// Confidence and importance filters
	query = query.Where("confidence >= ?", req.MinConfidence)
	if req.MinImportance > 0 {
		query = query.Where("importance >= ?", req.MinImportance)
	}

	// Execute query
	var items []model.MemoryItem
	if err := query.Limit(req.TopK * 2).Find(&items).Error; err != nil {
		return nil, memerrors.Wrap(memerrors.CodeInternal, "query failed", err)
	}

	// Score and rank results
	hits := s.scoreAndRank(items, req)

	// Limit to TopK
	if len(hits) > req.TopK {
		hits = hits[:req.TopK]
	}

	// Update access stats (async is fine, don't block return)
	go s.updateAccessStats(itemIDs)

	return hits, nil
}

// scoreAndRank calculates relevance scores for items.
func (s *MemoryService) scoreAndRank(items []model.MemoryItem, req RecallRequest) []MemoryHit {
	now := time.Now()
	hits := make([]MemoryHit, 0, len(items))

	for _, item := range items {
		hit := MemoryHit{MemoryItem: item}

		// FTS score (if query provided, items are pre-filtered by FTS)
		if req.Query != "" {
			hit.FTSScore = 1.0
			hit.MatchReasons = append(hit.MatchReasons, "text_match")
		}

		// Recency score (exponential decay)
		hoursAgo := now.Sub(item.CreatedAt).Hours()
		hit.RecencyScore = expDecay(hoursAgo, 168) // 7 days half-life

		// Importance score (normalized)
		hit.ImportanceScore = float64(item.Importance) / 100.0

		// Confidence score
		hit.ConfidenceScore = item.Confidence

		// Access boost
		accessBoost := min(0.1, float64(item.AccessCount)/100.0)

		// Combined score with weights
		hit.Score = 0.55*hit.FTSScore +
			0.20*hit.RecencyScore +
			0.15*hit.ImportanceScore +
			0.10*hit.ConfidenceScore +
			accessBoost

		hits = append(hits, hit)
	}

	// Sort by score descending
	// (Simplified - in production use sort.Slice)
	for i := 0; i < len(hits); i++ {
		for j := i + 1; j < len(hits); j++ {
			if hits[j].Score > hits[i].Score {
				hits[i], hits[j] = hits[j], hits[i]
			}
		}
	}

	return hits
}

func expDecay(hoursAgo, halfLifeHours float64) float64 {
	if hoursAgo < 0 {
		return 1.0
	}
	return 1.0 / (1.0 + hoursAgo/halfLifeHours)
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// updateAccessStats updates access count and last_access_at for items.
func (s *MemoryService) updateAccessStats(itemIDs []string) {
	if len(itemIDs) == 0 {
		return
	}
	now := time.Now()
	s.db.Model(&model.MemoryItem{}).
		Where("id IN ?", itemIDs).
		Updates(map[string]interface{}{
			"access_count":   gorm.Expr("access_count + 1"),
			"last_access_at": now,
		})
}

// ForgetRequest represents a request to forget memories.
type ForgetRequest struct {
	ItemIDs        []string
	Namespace      string
	NamespaceType  model.NamespaceType
	Mode           string // "soft" (default), "hard", "expire"
	Reason         string
}

// Forget removes or marks memories as deleted/expired.
func (s *MemoryService) Forget(ctx context.Context, req ForgetRequest) (int, error) {
	if req.Mode == "" {
		req.Mode = "soft"
	}

	// First, find the items to be deleted (for callbacks)
	var itemIDs []string
	if s.config.OnDeleted != nil {
		query := s.db.WithContext(ctx).Model(&model.MemoryItem{}).Select("id")
		if len(req.ItemIDs) > 0 {
			query = query.Where("id IN ?", req.ItemIDs)
		}
		if req.Namespace != "" {
			query = query.Where("namespace = ?", req.Namespace)
		}
		if req.NamespaceType != "" {
			query = query.Where("namespace_type = ?", req.NamespaceType)
		}
		query.Where("status = ?", model.ItemStatusActive).Pluck("id", &itemIDs)
	}

	dbQuery := s.db.WithContext(ctx).Model(&model.MemoryItem{})

	// Build where clause
	if len(req.ItemIDs) > 0 {
		dbQuery = dbQuery.Where("id IN ?", req.ItemIDs)
	}
	if req.Namespace != "" {
		dbQuery = dbQuery.Where("namespace = ?", req.Namespace)
	}
	if req.NamespaceType != "" {
		dbQuery = dbQuery.Where("namespace_type = ?", req.NamespaceType)
	}

	switch req.Mode {
	case "expire":
		// Mark as expired
		result := dbQuery.Update("status", model.ItemStatusExpired)
		if result.Error != nil {
			return 0, memerrors.Wrap(memerrors.CodeInternal, "expire failed", result.Error)
		}
		// Trigger callbacks
		for _, id := range itemIDs {
			if s.config.OnExpired != nil {
				s.config.OnExpired(ctx, id)
			}
		}
		return int(result.RowsAffected), nil

	case "hard":
		// Move to deleted_items first, then delete
		var items []model.MemoryItem
		if err := dbQuery.Find(&items).Error; err != nil {
			return 0, memerrors.Wrap(memerrors.CodeInternal, "find items failed", err)
		}
		for _, item := range items {
			data, _ := json.Marshal(item)
			deleted := model.DeletedItem{
				ID:               item.ID,
				OriginalDataJSON: string(data),
				DeletedAt:        time.Now(),
				PurgeAfter:       time.Now().Add(7 * 24 * time.Hour),
				Reason:           &req.Reason,
			}
			s.db.WithContext(ctx).Create(&deleted)
		}
		result := dbQuery.Delete(&model.MemoryItem{})
		if result.Error != nil {
			return 0, memerrors.Wrap(memerrors.CodeInternal, "hard delete failed", result.Error)
		}
		// Trigger callbacks
		for _, id := range itemIDs {
			if s.config.OnDeleted != nil {
				s.config.OnDeleted(ctx, id)
			}
		}
		return int(result.RowsAffected), nil

	default: // "soft"
		// Just mark status
		result := dbQuery.Update("status", model.ItemStatusDeleted)
		if result.Error != nil {
			return 0, memerrors.Wrap(memerrors.CodeInternal, "soft delete failed", result.Error)
		}
		// Trigger callbacks
		for _, id := range itemIDs {
			if s.config.OnDeleted != nil {
				s.config.OnDeleted(ctx, id)
			}
		}
		return int(result.RowsAffected), nil
	}
}

// UpdateRequest represents a request to update a memory.
type UpdateRequest struct {
	ItemID          string
	Title           *string
	Content         *string
	Summary         *string
	Tags            []string
	Importance      *int
	Confidence      *float64
	ExpectedVersion int // For optimistic locking
}

// Update modifies a memory item with optimistic locking.
func (s *MemoryService) Update(ctx context.Context, req UpdateRequest) error {
	var item model.MemoryItem
	if err := s.db.WithContext(ctx).First(&item, "id = ?", req.ItemID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return memerrors.Wrap(memerrors.CodeNotFound, "item not found", err)
		}
		return memerrors.Wrap(memerrors.CodeInternal, "query failed", err)
	}

	// Optimistic locking check
	if item.Version != req.ExpectedVersion {
		return memerrors.Wrap(memerrors.CodeConflict, fmt.Sprintf("version conflict: expected %d, got %d", req.ExpectedVersion, item.Version), nil)
	}

	// Build updates
	updates := map[string]interface{}{
		"version":   item.Version + 1,
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

	result := s.db.WithContext(ctx).Model(&item).Updates(updates)
	if result.Error != nil {
		return memerrors.Wrap(memerrors.CodeInternal, "update failed", result.Error)
	}
	if result.RowsAffected == 0 {
		return memerrors.New(memerrors.CodeInternal, "update failed: no rows affected")
	}

	// Reload item and trigger callback
	if s.config.OnUpdated != nil {
		s.db.WithContext(ctx).First(&item, "id = ?", req.ItemID)
		s.config.OnUpdated(ctx, item)
	}

	return nil
}

// Touch updates access statistics for an item (used for sliding TTL).
func (s *MemoryService) Touch(ctx context.Context, itemID string) error {
	now := time.Now()
	result := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("id = ?", itemID).
		Updates(map[string]interface{}{
			"access_count":   gorm.Expr("access_count + 1"),
			"last_access_at": now,
		})

	if result.Error != nil {
		return memerrors.Wrap(memerrors.CodeInternal, "touch failed", result.Error)
	}
	if result.RowsAffected == 0 {
		return memerrors.Wrap(memerrors.CodeNotFound, fmt.Sprintf("item not found: %s", itemID), nil)
	}

	return nil
}

// TouchWithRenew updates access stats and renews expiration for sliding TTL items.
// threshold: access count threshold to trigger renewal (e.g., every 3rd access)
// ttlSeconds: new TTL duration to set when renewing
func (s *MemoryService) TouchWithRenew(ctx context.Context, itemID string, threshold int, ttlSeconds int) (renewed bool, err error) {
	now := time.Now()

	// Get current item
	var item model.MemoryItem
	if err := s.db.WithContext(ctx).First(&item, "id = ?", itemID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, memerrors.Wrap(memerrors.CodeNotFound, "item not found", err)
		}
		return false, memerrors.Wrap(memerrors.CodeInternal, "query failed", err)
	}

	// Only renew if has expiry and threshold reached
	if item.ExpiresAt != nil && threshold > 0 && (item.AccessCount+1)%threshold == 0 {
		newExpiry := now.Add(time.Duration(ttlSeconds) * time.Second)
		result := s.db.WithContext(ctx).Model(&item).Updates(map[string]interface{}{
			"access_count":   gorm.Expr("access_count + 1"),
			"last_access_at": now,
			"expires_at":     newExpiry,
		})
		if result.Error != nil {
			return false, memerrors.Wrap(memerrors.CodeInternal, "renew failed", result.Error)
		}
		return true, nil
	}

	// Regular touch without renew
	return false, s.Touch(ctx, itemID)
}

// RenewExpiration manually renews the expiration time for an item.
func (s *MemoryService) RenewExpiration(ctx context.Context, itemID string, ttlSeconds int) error {
	newExpiry := time.Now().Add(time.Duration(ttlSeconds) * time.Second)
	result := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("id = ?", itemID).
		Update("expires_at", newExpiry)

	if result.Error != nil {
		return memerrors.Wrap(memerrors.CodeInternal, "renew expiration failed", result.Error)
	}
	if result.RowsAffected == 0 {
		return memerrors.Wrap(memerrors.CodeNotFound, fmt.Sprintf("item not found: %s", itemID), nil)
	}

	return nil
}

// CleanupExpired marks expired items as expired status (soft cleanup).
func (s *MemoryService) CleanupExpired(ctx context.Context) (int64, error) {
	now := time.Now()

	// Find expired items for callbacks
	var itemIDs []string
	if s.config.OnExpired != nil {
		s.db.WithContext(ctx).Model(&model.MemoryItem{}).
			Where("status = ? AND expires_at IS NOT NULL AND expires_at < ?", model.ItemStatusActive, now).
			Pluck("id", &itemIDs)
	}

	result := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("status = ? AND expires_at IS NOT NULL AND expires_at < ?", model.ItemStatusActive, now).
		Update("status", model.ItemStatusExpired)

	if result.Error != nil {
		return 0, memerrors.Wrap(memerrors.CodeInternal, "cleanup expired failed", result.Error)
	}

	// Trigger callbacks
	for _, id := range itemIDs {
		if s.config.OnExpired != nil {
			s.config.OnExpired(ctx, id)
		}
	}

	return result.RowsAffected, nil
}

// PurgeDeleted physically deletes soft-deleted items and moves them to deleted_items.
func (s *MemoryService) PurgeDeleted(ctx context.Context, before time.Time) (int64, error) {
	// Find items to purge
	var items []model.MemoryItem
	if err := s.db.WithContext(ctx).
		Where("status = ? AND updated_at < ?", model.ItemStatusDeleted, before).
		Find(&items).Error; err != nil {
		return 0, memerrors.Wrap(memerrors.CodeInternal, "find deleted items failed", err)
	}

	if len(items) == 0 {
		return 0, nil
	}

	// Move to deleted_items
	now := time.Now()
	purgeAfter := now.Add(7 * 24 * time.Hour)
	for _, item := range items {
		data, _ := json.Marshal(item)
		deleted := model.DeletedItem{
			ID:               item.ID,
			OriginalDataJSON: string(data),
			DeletedAt:        now,
			PurgeAfter:       purgeAfter,
		}
		if err := s.db.WithContext(ctx).Create(&deleted).Error; err != nil {
			// Log but continue
			continue
		}
	}

	// Delete from memory_items
	result := s.db.WithContext(ctx).
		Where("status = ? AND updated_at < ?", model.ItemStatusDeleted, before).
		Delete(&model.MemoryItem{})

	if result.Error != nil {
		return 0, memerrors.Wrap(memerrors.CodeInternal, "purge deleted failed", result.Error)
	}

	return result.RowsAffected, nil
}

// RebuildFTS rebuilds the FTS5 virtual table from scratch (emergency use).
func (s *MemoryService) RebuildFTS(ctx context.Context) error {
	// Delete all from FTS
	if err := s.db.WithContext(ctx).Exec("DELETE FROM fts_memory").Error; err != nil {
		return memerrors.Wrap(memerrors.CodeInternal, "clear fts failed", err)
	}

	// Re-insert all active items
	var items []model.MemoryItem
	if err := s.db.WithContext(ctx).Where("status = ?", model.ItemStatusActive).Find(&items).Error; err != nil {
		return memerrors.Wrap(memerrors.CodeInternal, "fetch items failed", err)
	}

	for _, item := range items {
		if err := s.db.WithContext(ctx).Exec(`
			INSERT INTO fts_memory (item_id, title, content, summary, tags_text)
			VALUES (?, COALESCE(?, ''), ?, COALESCE(?, ''), COALESCE(?, ''))
		`, item.ID, item.Title, item.Content, item.Summary, item.TagsJSON).Error; err != nil {
			// Continue on error
			continue
		}
	}

	return nil
}
