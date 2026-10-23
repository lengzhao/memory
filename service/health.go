package service

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
)

// Stats is an operational snapshot of the store. It is meant for health endpoints and for
// alerting on FTS drift (ActiveItems vs IndexedItems) and on the retention backlog
// (ArchivedItems / ExpiredArchiveItems).
type Stats struct {
	TotalItems          int64            `json:"total_items"`
	ActiveItems         int64            `json:"active_items"`
	ByNamespaceType     map[string]int64 `json:"by_namespace_type"`
	ExpiredPendingSweep int64            `json:"expired_pending_sweep"`
	SoftDeletedItems    int64            `json:"soft_deleted_items"`
	IndexedItems        int64            `json:"indexed_items"`
	IndexInSync         bool             `json:"index_in_sync"`
	ArchivedItems       int64            `json:"archived_items"`
	ExpiredArchiveItems int64            `json:"expired_archive_items"`
	Extractions         int64            `json:"extractions"`
	Events              int64            `json:"events"`
	Policies            int64            `json:"policies"`
}

// Stats collects the operational snapshot. It takes no isolation identity on purpose: it is an
// administrative view over the whole store.
func (s *MemoryService) Stats(ctx context.Context) (Stats, error) {
	stats := Stats{ByNamespaceType: map[string]int64{}}

	count := func(query *gorm.DB, target *int64) error {
		return query.Count(target).Error
	}

	if err := count(s.db.WithContext(ctx).Model(&model.MemoryItem{}), &stats.TotalItems); err != nil {
		return Stats{}, wrapErr(CodeInternal, "count items failed", err)
	}
	if err := count(s.db.WithContext(ctx).Model(&model.MemoryItem{}).Where("status = ?", model.ItemStatusActive), &stats.ActiveItems); err != nil {
		return Stats{}, wrapErr(CodeInternal, "count active items failed", err)
	}
	if err := count(s.db.WithContext(ctx).Model(&model.MemoryItem{}).Where("status = ?", model.ItemStatusDeleted), &stats.SoftDeletedItems); err != nil {
		return Stats{}, wrapErr(CodeInternal, "count deleted items failed", err)
	}
	if err := count(s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("status = ? AND expires_at IS NOT NULL AND expires_at < ?", model.ItemStatusActive, time.Now()),
		&stats.ExpiredPendingSweep); err != nil {
		return Stats{}, wrapErr(CodeInternal, "count expired items failed", err)
	}

	var byType []struct {
		NamespaceType string
		Total         int64
	}
	if err := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Select("namespace_type, COUNT(*) AS total").
		Group("namespace_type").
		Scan(&byType).Error; err != nil {
		return Stats{}, wrapErr(CodeInternal, "group items failed", err)
	}
	for _, row := range byType {
		stats.ByNamespaceType[row.NamespaceType] = row.Total
	}

	if err := s.db.WithContext(ctx).Raw("SELECT COUNT(*) FROM fts_memory").Scan(&stats.IndexedItems).Error; err != nil {
		return Stats{}, wrapErr(CodeInternal, "count fts rows failed", err)
	}
	stats.IndexInSync = stats.ActiveItems == stats.IndexedItems

	if err := count(s.db.WithContext(ctx).Model(&model.DeletedItem{}), &stats.ArchivedItems); err != nil {
		return Stats{}, wrapErr(CodeInternal, "count archived items failed", err)
	}
	if err := count(s.db.WithContext(ctx).Model(&model.DeletedItem{}).Where("purge_after <= ?", time.Now()), &stats.ExpiredArchiveItems); err != nil {
		return Stats{}, wrapErr(CodeInternal, "count expired archive failed", err)
	}
	if err := count(s.db.WithContext(ctx).Model(&model.DialogExtraction{}), &stats.Extractions); err != nil {
		return Stats{}, wrapErr(CodeInternal, "count extractions failed", err)
	}
	if err := count(s.db.WithContext(ctx).Model(&model.MemoryEvent{}), &stats.Events); err != nil {
		return Stats{}, wrapErr(CodeInternal, "count events failed", err)
	}
	if err := count(s.db.WithContext(ctx).Model(&model.NamespacePolicy{}), &stats.Policies); err != nil {
		return Stats{}, wrapErr(CodeInternal, "count policies failed", err)
	}

	return stats, nil
}

// Health reports whether the store is usable and the search index is consistent with it.
func (s *MemoryService) Health(ctx context.Context) error {
	if err := s.db.WithContext(ctx).Exec("SELECT 1").Error; err != nil {
		return wrapErr(CodeInternal, "database unavailable", err)
	}
	ok, err := s.ValidateFTS(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return wrapErr(CodeInternal, "fts index out of sync: run RebuildFTS or re-run Migrate", nil)
	}
	return nil
}
