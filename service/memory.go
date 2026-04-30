// Package service provides the core memory service implementation.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
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

// WithConfig sets the configuration for the memory service.
// Returns the service for method chaining.
func (s *MemoryService) WithConfig(config Config) *MemoryService {
	s.config = config
	return s
}

// RememberRequest represents a request to store a memory.
type RememberRequest struct {
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
	if req.NamespaceType == "" {
		req.NamespaceType = model.NamespaceTypeTransient
	}

	namespace := ""
	if isIsolationEnabled(ctx) {
		meta, err := IsolationFromContext(ctx)
		if err != nil {
			return "", err
		}
		namespace = buildNamespace(meta, req.NamespaceType)
		if req.DedupeKey != nil && strings.TrimSpace(*req.DedupeKey) != "" {
			prefixed := fmt.Sprintf("%s:%s:%s:%s:%s",
				meta.TenantID, meta.UserID, meta.SessionID, meta.AgentID, strings.TrimSpace(*req.DedupeKey))
			req.DedupeKey = &prefixed
		}
	} else {
		namespace = buildDefaultNamespace(req.NamespaceType)
	}

	now := time.Now()

	// Check for duplicate by dedupe_key if provided
	if req.DedupeKey != nil && *req.DedupeKey != "" {
		var existing model.MemoryItem
		err := s.db.WithContext(ctx).
			Where("namespace = ? AND dedupe_key = ?", namespace, *req.DedupeKey).
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

	// Create new item with tokenized text for search
	tagsJSON, _ := json.Marshal(req.Tags)
	textToTokenize := req.Title + " " + req.Content + " " + req.Summary
	item := model.MemoryItem{
		ID:            model.GenerateID(),
		Namespace:     namespace,
		NamespaceType: req.NamespaceType,
		Title:         req.Title,
		Content:       req.Content,
		Summary:       req.Summary,
		TagsJSON:      string(tagsJSON),
		TokenizedText: TokenizeForSearch(textToTokenize),
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
		return "", wrapErr(CodeInternal, "failed to create memory", err)
	}

	// Trigger callback
	if s.config.OnCreated != nil {
		s.config.OnCreated(ctx, item)
	}

	return item.ID, nil
}

// RecallRequest represents a request to recall memories.
type RecallRequest struct {
	Query          string
	Namespaces     []string
	NamespaceTypes []model.NamespaceType
	TagsAny        []string
	TagsAll        []string
	TimeRangeStart *time.Time
	TimeRangeEnd   *time.Time
	IncludeExpired bool
	MinConfidence  float64
	MinImportance  int
	TopK           int
	ExcludeItemIDs []string
}

// ListRequest represents a request to list memories by time.
type ListRequest struct {
	Namespaces     []string
	NamespaceTypes []model.NamespaceType
	TagsAny        []string
	TagsAll        []string
	TimeRangeStart *time.Time
	TimeRangeEnd   *time.Time
	IncludeExpired bool
	MinConfidence  float64
	MinImportance  int
	TopK           int
	Offset         int
	ExcludeItemIDs []string
	Order          string // "desc" (default) or "asc", based on created_at
}

// MemoryHit represents a recalled memory with relevance info.
type MemoryHit struct {
	model.MemoryItem
	Score           float64
	FTSScore        float64
	RecencyScore    float64
	ImportanceScore float64
	ConfidenceScore float64
	MatchReasons    []string
}

// Recall searches for memories using FTS and filtering.
func (s *MemoryService) Recall(ctx context.Context, req RecallRequest) ([]MemoryHit, error) {
	if isIsolationEnabled(ctx) {
		meta, err := IsolationFromContext(ctx)
		if err != nil {
			return nil, err
		}
		if len(req.Namespaces) > 0 {
			return nil, newErr(CodeValidation, "namespaces must not be provided when context isolation is enabled")
		}
		req.Namespaces = buildAllowedNamespaces(meta, req.NamespaceTypes)
	}

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

	// Text search if query provided
	var itemIDs []string
	var useLike bool
	if req.Query != "" {
		itemIDs, useLike = s.searchText(ctx, req.Query, req.TopK*3)
		if useLike {
			// CJK fallback: use LIKE for Chinese/Japanese/Korean queries
			pattern := "%" + req.Query + "%"
			query = query.Where(
				"title LIKE ? OR content LIKE ? OR summary LIKE ?",
				pattern, pattern, pattern,
			)
		} else {
			// FTS results
			if len(itemIDs) == 0 {
				return []MemoryHit{}, nil
			}
			query = query.Where("id IN ?", itemIDs)
		}
	}

	// Confidence and importance filters
	query = query.Where("confidence >= ?", req.MinConfidence)
	if req.MinImportance > 0 {
		query = query.Where("importance >= ?", req.MinImportance)
	}

	// Execute query
	var items []model.MemoryItem
	if err := query.Limit(req.TopK * 2).Find(&items).Error; err != nil {
		return nil, wrapErr(CodeInternal, "query failed", err)
	}

	// Score and rank results
	hits := s.scoreAndRank(items, req)

	// Limit to TopK
	if len(hits) > req.TopK {
		hits = hits[:req.TopK]
	}

	// Update access stats (async is fine, don't block return)
	// For LIKE fallback, extract IDs from hits
	if len(itemIDs) == 0 && len(hits) > 0 {
		itemIDs = make([]string, len(hits))
		for i, h := range hits {
			itemIDs[i] = h.ID
		}
	}
	go s.updateAccessStats(itemIDs)

	return hits, nil
}

// List returns memories ordered by created_at.
func (s *MemoryService) List(ctx context.Context, req ListRequest) ([]model.MemoryItem, error) {
	if isIsolationEnabled(ctx) {
		meta, err := IsolationFromContext(ctx)
		if err != nil {
			return nil, err
		}
		if len(req.Namespaces) > 0 {
			return nil, newErr(CodeValidation, "namespaces must not be provided when context isolation is enabled")
		}
		req.Namespaces = buildAllowedNamespaces(meta, req.NamespaceTypes)
	}

	if req.TopK <= 0 {
		req.TopK = 10
	}
	if req.MinConfidence <= 0 {
		req.MinConfidence = 0.5
	}

	query := s.db.WithContext(ctx).Model(&model.MemoryItem{})
	query = query.Where("status = ?", model.ItemStatusActive)

	if !req.IncludeExpired {
		query = query.Where("expires_at IS NULL OR expires_at > ?", time.Now())
	}

	if len(req.Namespaces) > 0 {
		query = query.Where("namespace IN ?", req.Namespaces)
	}
	if len(req.NamespaceTypes) > 0 {
		query = query.Where("namespace_type IN ?", req.NamespaceTypes)
	}

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

	if req.TimeRangeStart != nil {
		query = query.Where("created_at >= ?", *req.TimeRangeStart)
	}
	if req.TimeRangeEnd != nil {
		query = query.Where("created_at <= ?", *req.TimeRangeEnd)
	}

	if len(req.ExcludeItemIDs) > 0 {
		query = query.Where("id NOT IN ?", req.ExcludeItemIDs)
	}

	query = query.Where("confidence >= ?", req.MinConfidence)
	if req.MinImportance > 0 {
		query = query.Where("importance >= ?", req.MinImportance)
	}

	orderDirection := "DESC"
	if strings.EqualFold(req.Order, "asc") {
		orderDirection = "ASC"
	}

	var items []model.MemoryItem
	if err := query.
		Order("created_at " + orderDirection).
		Offset(req.Offset).
		Limit(req.TopK).
		Find(&items).Error; err != nil {
		return nil, wrapErr(CodeInternal, "list query failed", err)
	}

	return items, nil
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
	sort.Slice(hits, func(i, j int) bool {
		return hits[i].Score > hits[j].Score
	})

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

// searchText performs text search using pre-tokenized FTS5 index.
// The query is tokenized using jiebago for CJK support.
func (s *MemoryService) searchText(ctx context.Context, query string, limit int) ([]string, bool) {
	// Tokenize the query (handles both CJK and English)
	tokenizedQuery := TokenizeQuery(query)
	if tokenizedQuery == "" {
		// No valid tokens, try LIKE fallback
		return nil, true
	}

	itemIDs := s.searchFTS(ctx, tokenizedQuery, limit)
	if len(itemIDs) == 0 {
		// FTS found nothing, try LIKE fallback
		return nil, true
	}
	return itemIDs, false
}

// searchFTS performs FTS5 search on pre-tokenized content.
// Uses OR query to match any token (e.g., "清华 OR 大学" matches "北京 清华大学")
func (s *MemoryService) searchFTS(ctx context.Context, tokenizedQuery string, limit int) []string {
	// Build OR query from space-separated tokens
	tokens := strings.Fields(tokenizedQuery)
	if len(tokens) == 0 {
		return nil
	}

	// Create OR query: "token1 OR token2 OR token3"
	var orQuery strings.Builder
	for i, token := range tokens {
		if i > 0 {
			orQuery.WriteString(" OR ")
		}
		orQuery.WriteString("\"" + token + "\"")
	}

	var results []struct {
		ItemID string
	}
	err := s.db.WithContext(ctx).Raw(`
		SELECT item_id FROM fts_memory 
		WHERE tokenized_content MATCH ? 
		LIMIT ?
	`, orQuery.String(), limit).Scan(&results).Error
	if err != nil {
		return nil
	}

	itemIDs := make([]string, len(results))
	for i, r := range results {
		itemIDs[i] = r.ItemID
	}
	return itemIDs
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
	ItemIDs       []string
	Namespace     string
	NamespaceType model.NamespaceType
	Mode          string // "soft" (default), "hard", "expire"
	Reason        string
}

// Forget removes or marks memories as deleted/expired.
func (s *MemoryService) Forget(ctx context.Context, req ForgetRequest) (int, error) {
	if isIsolationEnabled(ctx) {
		meta, err := IsolationFromContext(ctx)
		if err != nil {
			return 0, err
		}
		if strings.TrimSpace(req.Namespace) != "" {
			return 0, newErr(CodeValidation, "namespace must not be provided when context isolation is enabled")
		}
		req.Namespace = buildNamespace(meta, req.NamespaceType)
	}

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
			return 0, wrapErr(CodeInternal, "expire failed", result.Error)
		}
		// Trigger callbacks
		for _, id := range itemIDs {
			if s.config.OnExpired != nil {
				s.config.OnExpired(ctx, id)
			}
		}
		return int(result.RowsAffected), nil

	case "hard":
		var rowsAffected int64
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// Move to deleted_items first, then delete
			var items []model.MemoryItem
			txQuery := tx.Model(&model.MemoryItem{})
			if len(req.ItemIDs) > 0 {
				txQuery = txQuery.Where("id IN ?", req.ItemIDs)
			}
			if req.Namespace != "" {
				txQuery = txQuery.Where("namespace = ?", req.Namespace)
			}
			if req.NamespaceType != "" {
				txQuery = txQuery.Where("namespace_type = ?", req.NamespaceType)
			}
			if err := txQuery.Find(&items).Error; err != nil {
				return wrapErr(CodeInternal, "find items failed", err)
			}

			now := time.Now()
			purgeAfter := now.Add(7 * 24 * time.Hour)
			for _, item := range items {
				data, _ := json.Marshal(item)
				deleted := model.DeletedItem{
					ID:               item.ID,
					OriginalDataJSON: string(data),
					DeletedAt:        now,
					PurgeAfter:       purgeAfter,
					Reason:           &req.Reason,
				}
				if err := tx.Create(&deleted).Error; err != nil {
					return wrapErr(CodeInternal, "backup before hard delete failed", err)
				}
			}

			result := txQuery.Delete(&model.MemoryItem{})
			if result.Error != nil {
				return wrapErr(CodeInternal, "hard delete failed", result.Error)
			}
			rowsAffected = result.RowsAffected
			return nil
		}); err != nil {
			return 0, err
		}
		// Trigger callbacks
		for _, id := range itemIDs {
			if s.config.OnDeleted != nil {
				s.config.OnDeleted(ctx, id)
			}
		}
		return int(rowsAffected), nil

	default: // "soft"
		// Just mark status
		result := dbQuery.Update("status", model.ItemStatusDeleted)
		if result.Error != nil {
			return 0, wrapErr(CodeInternal, "soft delete failed", result.Error)
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
			return wrapErr(CodeNotFound, "item not found", err)
		}
		return wrapErr(CodeInternal, "query failed", err)
	}

	// Optimistic locking check
	if item.Version != req.ExpectedVersion {
		return wrapErr(CodeConflict, fmt.Sprintf("version conflict: expected %d, got %d", req.ExpectedVersion, item.Version), nil)
	}

	// Build updates
	updates := map[string]interface{}{
		"version":    item.Version + 1,
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

	// Re-tokenize if text fields changed
	if req.Title != nil || req.Content != nil || req.Summary != nil {
		title := item.Title
		content := item.Content
		summary := item.Summary
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

	result := s.db.WithContext(ctx).Model(&item).Updates(updates)
	if result.Error != nil {
		return wrapErr(CodeInternal, "update failed", result.Error)
	}
	if result.RowsAffected == 0 {
		return newErr(CodeInternal, "update failed: no rows affected")
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
		return wrapErr(CodeInternal, "touch failed", result.Error)
	}
	if result.RowsAffected == 0 {
		return wrapErr(CodeNotFound, fmt.Sprintf("item not found: %s", itemID), nil)
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
			return false, wrapErr(CodeNotFound, "item not found", err)
		}
		return false, wrapErr(CodeInternal, "query failed", err)
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
			return false, wrapErr(CodeInternal, "renew failed", result.Error)
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
		return wrapErr(CodeInternal, "renew expiration failed", result.Error)
	}
	if result.RowsAffected == 0 {
		return wrapErr(CodeNotFound, fmt.Sprintf("item not found: %s", itemID), nil)
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
		return 0, wrapErr(CodeInternal, "cleanup expired failed", result.Error)
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
	var rowsAffected int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Find items to purge
		var items []model.MemoryItem
		if err := tx.
			Where("status = ? AND updated_at < ?", model.ItemStatusDeleted, before).
			Find(&items).Error; err != nil {
			return wrapErr(CodeInternal, "find deleted items failed", err)
		}
		if len(items) == 0 {
			rowsAffected = 0
			return nil
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
			if err := tx.Create(&deleted).Error; err != nil {
				return wrapErr(CodeInternal, "archive deleted item failed", err)
			}
		}

		// Delete from memory_items
		result := tx.
			Where("status = ? AND updated_at < ?", model.ItemStatusDeleted, before).
			Delete(&model.MemoryItem{})
		if result.Error != nil {
			return wrapErr(CodeInternal, "purge deleted failed", result.Error)
		}
		rowsAffected = result.RowsAffected
		return nil
	})
	if err != nil {
		return 0, err
	}
	return rowsAffected, nil
}

// RebuildFTS rebuilds the FTS5 virtual table from scratch (emergency use).
func (s *MemoryService) RebuildFTS(ctx context.Context) error {
	// Delete all from FTS
	if err := s.db.WithContext(ctx).Exec("DELETE FROM fts_memory").Error; err != nil {
		return wrapErr(CodeInternal, "clear fts failed", err)
	}

	// Re-insert all active items
	var items []model.MemoryItem
	if err := s.db.WithContext(ctx).Where("status = ?", model.ItemStatusActive).Find(&items).Error; err != nil {
		return wrapErr(CodeInternal, "fetch items failed", err)
	}

	for _, item := range items {
		if err := s.db.WithContext(ctx).Exec(`
			INSERT INTO fts_memory (tokenized_content, item_id)
			VALUES (?, ?)
		`, item.TokenizedText, item.ID).Error; err != nil {
			// Continue on error
			continue
		}
	}

	return nil
}
