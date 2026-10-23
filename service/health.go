package service

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
)

// Stats is an operational snapshot of the store, meant for health endpoints and for alerting on
// search-index drift (IndexInSync) and on the deletion-retention backlog (ExpiredArchiveItems).
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
	ExtractionsPending  int64            `json:"extractions_pending"`
	ExtractionFailures  int64            `json:"extraction_failures"`
	Events              int64            `json:"events"`
	Policies            int64            `json:"policies"`
}

// Stats collects the snapshot. It is deliberately scope-agnostic: this is an administrative view
// of the whole store, not a user-facing read.
func (s *MemoryService) Stats(ctx context.Context) (Stats, error) {
	stats := Stats{ByNamespaceType: map[string]int64{}}

	count := func(query *gorm.DB, target *int64) error {
		if err := query.Count(target).Error; err != nil {
			return wrapErr(CodeInternal, "count failed", err)
		}
		return nil
	}

	if err := count(s.db.WithContext(ctx).Model(&model.MemoryItem{}), &stats.TotalItems); err != nil {
		return Stats{}, err
	}
	if err := count(s.db.WithContext(ctx).Model(&model.MemoryItem{}).Where("status = ?", model.ItemStatusActive), &stats.ActiveItems); err != nil {
		return Stats{}, err
	}
	if err := count(s.db.WithContext(ctx).Model(&model.MemoryItem{}).Where("status = ?", model.ItemStatusDeleted), &stats.SoftDeletedItems); err != nil {
		return Stats{}, err
	}
	if err := count(s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("status = ? AND expires_at IS NOT NULL AND expires_at < ?", model.ItemStatusActive, time.Now()),
		&stats.ExpiredPendingSweep); err != nil {
		return Stats{}, err
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
		return Stats{}, wrapErr(CodeInternal, "count indexed rows failed", err)
	}
	stats.IndexInSync = stats.ActiveItems == stats.IndexedItems

	if err := count(s.db.WithContext(ctx).Model(&model.DeletedItem{}), &stats.ArchivedItems); err != nil {
		return Stats{}, err
	}
	if err := count(s.db.WithContext(ctx).Model(&model.DeletedItem{}).Where("purge_after <= ?", time.Now()), &stats.ExpiredArchiveItems); err != nil {
		return Stats{}, err
	}
	if err := count(s.db.WithContext(ctx).Model(&model.DialogExtraction{}), &stats.Extractions); err != nil {
		return Stats{}, err
	}
	if err := count(s.db.WithContext(ctx).Model(&model.DialogExtraction{}).Where("status = ?", model.ExtractionStatusPending), &stats.ExtractionsPending); err != nil {
		return Stats{}, err
	}
	if err := count(s.db.WithContext(ctx).Model(&model.DialogExtraction{}).Where("status = ?", model.ExtractionStatusFailed), &stats.ExtractionFailures); err != nil {
		return Stats{}, err
	}
	if err := count(s.db.WithContext(ctx).Model(&model.MemoryEvent{}), &stats.Events); err != nil {
		return Stats{}, err
	}
	if err := count(s.db.WithContext(ctx).Model(&model.NamespacePolicy{}), &stats.Policies); err != nil {
		return Stats{}, err
	}
	return stats, nil
}

// Health reports whether the store is usable and the search index agrees with it.
func (s *MemoryService) Health(ctx context.Context) error {
	if err := s.db.WithContext(ctx).Exec("SELECT 1").Error; err != nil {
		return wrapErr(CodeInternal, "database unavailable", err)
	}
	ok, err := s.ValidateFTS(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return wrapErr(CodeInternal, "search index out of sync: run RebuildFTS or Migrate", nil)
	}
	return nil
}
