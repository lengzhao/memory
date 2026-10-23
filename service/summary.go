// Package service provides summary generation for memory items and namespaces.
package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
)

// SummaryGenerator produces the per-item summary stored alongside a memory. The stored summary is
// what RecallContext uses when IncludeSummaryText is set, and it is derived locally: it never calls
// an LLM. A host that wants model-written summaries passes them as RememberRequest.Summary (or via
// UpdateRequest) instead of calling this.
type SummaryGenerator struct {
	db *gorm.DB
}

// NewSummaryGenerator creates a new summary generator.
func NewSummaryGenerator(db *gorm.DB) *SummaryGenerator {
	return &SummaryGenerator{db: db}
}

// GenerateItemSummary derives and stores a summary for one memory.
//
// It prefers the leading sentences of the content (where a writer usually states the point) over a
// hard cut, and it keeps the summary under maxTokens when a positive budget is given.
func (g *SummaryGenerator) GenerateItemSummary(ctx context.Context, scope Scope, itemID string, maxTokens int) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if maxTokens <= 0 {
		maxTokens = defaultItemSummaryTokens
	}

	var item model.MemoryItem
	if err := g.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("id = ?", itemID).
		Where("namespace IN ?", scope.Namespaces()).
		First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return wrapErr(CodeNotFound, "memory not found in this scope", err)
		}
		return wrapErr(CodeInternal, "query failed", err)
	}

	summary := summarizeText(item.Title, item.Content, maxTokens)
	if summary == "" {
		return wrapErr(CodeNotFound, "memory has no summarisable content", nil)
	}

	if err := g.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("id = ?", itemID).
		Updates(map[string]interface{}{
			"summary":    summary,
			"updated_at": time.Now(),
		}).Error; err != nil {
		return wrapErr(CodeInternal, "update summary failed", err)
	}
	return nil
}

// defaultItemSummaryTokens keeps a derived summary short enough to be cheap in a prompt.
const defaultItemSummaryTokens = 60

// summarizeText derives a summary: the title plus whole sentences from the head of the content,
// trimmed to the token budget without splitting a sentence when one fits.
func summarizeText(title, content string, maxTokens int) string {
	title = strings.TrimSpace(title)
	content = strings.TrimSpace(content)

	body := firstSentences(content, maxTokens)
	if title == "" {
		return trimToTokens(body, maxTokens)
	}
	if body == "" {
		return trimToTokens(title, maxTokens)
	}
	combined := title + ": " + body
	if tokenCounter(combined) <= maxTokens {
		return combined
	}
	// Give the title a quarter of the budget and spend the rest on the body.
	titleBudget := maxTokens / 4
	if titleBudget < 8 {
		titleBudget = 8
	}
	titlePart := trimToTokens(title, titleBudget)
	remaining := maxTokens - tokenCounter(titlePart) - 2
	if remaining <= 0 {
		return titlePart
	}
	return titlePart + ": " + trimToTokens(body, remaining)
}

// firstSentences returns the leading sentences of text that fit into maxTokens. Sentence boundaries
// are recognised for both CJK (。！？；) and Latin (., !, ?) text.
func firstSentences(text string, maxTokens int) string {
	if tokenCounter(text) <= maxTokens {
		return text
	}

	var builder strings.Builder
	for _, sentence := range splitSentences(text) {
		candidate := strings.TrimSpace(builder.String() + sentence)
		if tokenCounter(candidate) > maxTokens {
			break
		}
		builder.WriteString(sentence)
	}
	result := strings.TrimSpace(builder.String())
	if result == "" {
		return trimToTokens(text, maxTokens)
	}
	return result
}

func splitSentences(text string) []string {
	const terminators = "。！？；!?;\n"

	sentences := make([]string, 0, 4)
	var current strings.Builder
	for _, r := range text {
		current.WriteRune(r)
		if strings.ContainsRune(terminators, r) {
			sentences = append(sentences, current.String())
			current.Reset()
		}
	}
	if rest := strings.TrimSpace(current.String()); rest != "" {
		sentences = append(sentences, rest)
	}
	if len(sentences) == 0 && strings.TrimSpace(text) != "" {
		sentences = append(sentences, strings.TrimSpace(text))
	}
	return sentences
}
