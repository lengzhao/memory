// Package service provides summary generation for memory items and namespaces.
package service

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
	memerrors "github.com/lengzhao/memory/pkg/errors"
)

// SummaryGenerator handles summary generation for items and namespaces.
type SummaryGenerator struct {
	db *gorm.DB
}

// NewSummaryGenerator creates a new summary generator.
func NewSummaryGenerator(db *gorm.DB) *SummaryGenerator {
	return &SummaryGenerator{db: db}
}

// GenerateItemSummary generates a summary for a single memory item.
// In production, this would call an LLM to summarize. For now, it truncates content.
func (g *SummaryGenerator) GenerateItemSummary(ctx context.Context, itemID string) error {
	var item model.MemoryItem
	if err := g.db.WithContext(ctx).First(&item, "id = ?", itemID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return memerrors.Wrap(memerrors.CodeNotFound, "item not found", err)
		}
		return memerrors.Wrap(memerrors.CodeInternal, "query failed", err)
	}

	// Generate summary (simple truncation for now)
	summary := generateSummary(item.Content, 100)

	result := g.db.WithContext(ctx).Model(&item).Update("summary", summary)
	if result.Error != nil {
		return memerrors.Wrap(memerrors.CodeInternal, "update summary failed", result.Error)
	}

	return nil
}

// GenerateNamespaceSummary generates a summary for a namespace.
func (g *SummaryGenerator) GenerateNamespaceSummary(ctx context.Context, namespace string) (string, error) {
	// Get items in namespace
	var items []model.MemoryItem
	if err := g.db.WithContext(ctx).
		Where("namespace = ? AND status = ?", namespace, model.ItemStatusActive).
		Order("created_at DESC").
		Limit(50).
		Find(&items).Error; err != nil {
		return "", memerrors.Wrap(memerrors.CodeInternal, "query items failed", err)
	}

	if len(items) == 0 {
		return "", memerrors.Wrap(memerrors.CodeNotFound, "no items in namespace", nil)
	}

	// Generate summary (simple concatenation for now)
	summary := fmt.Sprintf("Namespace %s contains %d items. Recent: ", namespace, len(items))
	for i, item := range items {
		if i >= 3 {
			break
		}
		if i > 0 {
			summary += ", "
		}
		summary += item.Title
	}

	// Update or create namespace summary
	now := time.Now()
	var nsSummary model.NamespaceSummary
	err := g.db.WithContext(ctx).Where("namespace = ?", namespace).First(&nsSummary).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			// Create new
			nsSummary = model.NamespaceSummary{
				ID:        model.GenerateID(),
				Namespace: namespace,
				Summary:   summary,
				ItemCount: len(items),
				UpdatedAt: now,
			}
			if err := g.db.WithContext(ctx).Create(&nsSummary).Error; err != nil {
				return "", memerrors.Wrap(memerrors.CodeInternal, "create summary failed", err)
			}
			return summary, nil
		}
		return "", memerrors.Wrap(memerrors.CodeInternal, "query summary failed", err)
	}

	// Update existing
	nsSummary.Summary = summary
	nsSummary.ItemCount = len(items)
	nsSummary.UpdatedAt = now
	if err := g.db.WithContext(ctx).Save(&nsSummary).Error; err != nil {
		return "", memerrors.Wrap(memerrors.CodeInternal, "update summary failed", err)
	}

	return summary, nil
}

// generateSummary creates a simple summary by truncating text.
// In production, this would use an LLM for intelligent summarization.
func generateSummary(content string, maxLen int) string {
	if len(content) <= maxLen {
		return content
	}
	return content[:maxLen-3] + "..."
}
