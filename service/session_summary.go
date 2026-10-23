package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
)

// Session summaries: the rolling digest of one conversation.
//
// A session digest answers a question the memory index cannot: "what has already happened in this
// conversation?". The stored memories cover durable facts; the digest covers the narrative the
// recent turns are continuing from.
//
// The digest is produced in one of three ways, richest first:
//
//	SourceLLM    the extraction model emits it alongside the memories (no extra API call)
//	SourceHost   the application writes it (a host that already summarises its own turns)
//	SourceRollup a deterministic local aggregate of the session's memories (no LLM at all)
//
// The library only stores, versions and assembles it. It never calls an LLM on its own.

// SessionSummary is a session digest with its metadata.
type SessionSummary struct {
	TenantID    string
	UserID      string
	SessionID   string
	AgentID     string
	Summary     string
	Revision    int64
	Source      string
	Extractions int
	TokenCount  int
	UpdatedAt   time.Time
}

// sessionSummaryID derives a stable primary key, so a session has exactly one current digest.
func sessionSummaryID(scopeKey, sessionID string) string {
	sum := sha256.Sum256([]byte(scopeKey + "\x00" + sessionID))
	return "ss_" + hex.EncodeToString(sum[:16])
}

func validateSessionSummaryText(summary string) (string, error) {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "", newErr(CodeValidation, "summary must not be empty")
	}
	if len([]rune(summary)) > MaxSummaryRunes {
		return "", newErr(CodeValidation, "summary exceeds "+itoa(MaxSummaryRunes)+" runes")
	}
	return summary, nil
}

// MaxSummaryRunes bounds a session digest. A digest is meant to be a compact briefing, not a
// transcript: anything that needs the full history should read the memories themselves.
const MaxSummaryRunes = 8000

// UpsertSessionSummary stores a session digest, replacing the previous revision. Callers that only
// want to fill a gap use EnsureSessionSummary.
func (s *MemoryService) UpsertSessionSummary(ctx context.Context, scope Scope, summary, source string) (*SessionSummary, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if scope.IsGlobal() {
		return nil, newErr(CodeValidation, "session summaries need a session: the global scope has none")
	}
	text, err := validateSessionSummaryText(summary)
	if err != nil {
		return nil, err
	}
	if source == "" {
		source = model.SessionSummarySourceHost
	}
	return s.writeSessionSummary(ctx, scope, text, source, -1)
}

// SetSessionSummary is UpsertSessionSummary for hosts that produce their own digest: it writes the
// text verbatim (SourceHost) and returns the stored digest.
func (s *MemoryService) SetSessionSummary(ctx context.Context, scope Scope, summary string) (*SessionSummary, error) {
	return s.UpsertSessionSummary(ctx, scope, summary, model.SessionSummarySourceHost)
}

// GetSessionSummary returns the stored digest, or (nil, nil) when the session has none. A session
// without a digest is a normal state, not an error.
func (s *MemoryService) GetSessionSummary(ctx context.Context, scope Scope) (*SessionSummary, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	return s.loadSessionSummary(ctx, scope)
}

// EnsureSessionSummary returns the existing digest, or builds the deterministic local rollup when
// there is none. Use it when a host wants "a briefing even before the first model-written one".
func (s *MemoryService) EnsureSessionSummary(ctx context.Context, scope Scope, maxTokens int) (*SessionSummary, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	existing, err := s.loadSessionSummary(ctx, scope)
	if err != nil || existing != nil {
		return existing, err
	}
	return s.RefreshSessionSummaryRollup(ctx, scope, maxTokens)
}

// RefreshSessionSummaryRollup (re)builds the deterministic digest of a session from its memories:
// no LLM, no network. It states how much the session holds, the most important entries and the
// topics seen, which is enough to tell a model what the conversation has been about.
func (s *MemoryService) RefreshSessionSummaryRollup(ctx context.Context, scope Scope, maxTokens int) (*SessionSummary, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if scope.IsGlobal() {
		return nil, newErr(CodeValidation, "session summaries need a session: the global scope has none")
	}

	items, err := s.sessionItems(ctx, scope)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}

	summary := buildSessionRollup(items, maxTokens)
	if summary == "" {
		return nil, nil
	}
	stored, err := s.writeSessionSummary(ctx, scope, summary, model.SessionSummarySourceRollup, -1)
	if err != nil {
		return nil, err
	}
	// Trust the caller's budget over the rollup's own sizing.
	if maxTokens > 0 {
		stored.TokenCount = tokenCounter(stored.Summary)
	}
	return stored, nil
}

// ClearSessionSummary removes the digest of a session (e.g. after the session ends). Removing a
// digest does not touch the memories themselves.
func (s *MemoryService) ClearSessionSummary(ctx context.Context, scope Scope) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if scope.IsGlobal() {
		return newErr(CodeValidation, "session summaries need a session: the global scope has none")
	}
	result := s.db.WithContext(ctx).
		Where("scope_key = ? AND session_id = ?", scope.String(), scope.SessionID).
		Delete(&model.SessionSummary{})
	if result.Error != nil {
		return wrapErr(CodeInternal, "failed to clear session summary", result.Error)
	}
	if result.RowsAffected == 0 {
		return wrapErr(CodeNotFound, "no session summary to clear", nil)
	}
	return nil
}

// writeSessionSummary upserts one digest. extractions < 0 keeps the previous count (the caller has
// no new extraction to account for).
func (s *MemoryService) writeSessionSummary(ctx context.Context, scope Scope, summary, source string, extractions int) (*SessionSummary, error) {
	now := time.Now()
	scopeKey := scope.String()
	id := sessionSummaryID(scopeKey, scope.SessionID)
	tokenCount := tokenCounter(summary)

	var existing model.SessionSummary
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		revision := int64(1)
		row := model.SessionSummary{
			ID:          id,
			ScopeKey:    scopeKey,
			TenantID:    scope.TenantID,
			UserID:      scope.UserID,
			SessionID:   scope.SessionID,
			AgentID:     scope.AgentID,
			Summary:     summary,
			TokenCount:  tokenCount,
			Revision:    revision,
			Source:      source,
			Extractions: maxInt(extractions, 0),
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := createRow(ctx, s.db, &row); err != nil {
			return nil, wrapErr(CodeInternal, "failed to store session summary", err)
		}
		return sessionSummaryFromRow(row), nil
	case err != nil:
		return nil, wrapErr(CodeInternal, "failed to load session summary", err)
	}

	revision := existing.Revision + 1
	extractionCount := existing.Extractions
	if extractions >= 0 {
		extractionCount = extractions
	}
	// An explicit column map keeps the zero-legitimate fields (token_count) honest instead of being
	// skipped as "unset".
	if err := s.db.WithContext(ctx).Model(&model.SessionSummary{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"summary":     summary,
			"token_count": tokenCount,
			"revision":    revision,
			"source":      source,
			"extractions": extractionCount,
			"updated_at":  now,
		}).Error; err != nil {
		return nil, wrapErr(CodeInternal, "failed to update session summary", err)
	}

	return sessionSummaryFromRow(model.SessionSummary{
		ID: id, ScopeKey: scopeKey,
		TenantID: scope.TenantID, UserID: scope.UserID, SessionID: scope.SessionID, AgentID: scope.AgentID,
		Summary: summary, TokenCount: tokenCount, Revision: revision, Source: source,
		Extractions: extractionCount, CreatedAt: existing.CreatedAt, UpdatedAt: now,
	}), nil
}

func (s *MemoryService) loadSessionSummary(ctx context.Context, scope Scope) (*SessionSummary, error) {
	if scope.IsGlobal() {
		return nil, nil
	}
	var row model.SessionSummary
	err := s.db.WithContext(ctx).
		Where("scope_key = ? AND session_id = ?", scope.String(), scope.SessionID).
		First(&row).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	case err != nil:
		return nil, wrapErr(CodeInternal, "failed to load session summary", err)
	}
	return sessionSummaryFromRow(row), nil
}

func sessionSummaryFromRow(row model.SessionSummary) *SessionSummary {
	summary := &SessionSummary{
		TenantID:    row.TenantID,
		UserID:      row.UserID,
		SessionID:   row.SessionID,
		AgentID:     row.AgentID,
		Summary:     row.Summary,
		Revision:    row.Revision,
		Source:      row.Source,
		Extractions: row.Extractions,
		UpdatedAt:   row.UpdatedAt,
	}
	if row.TokenCount > 0 {
		summary.TokenCount = row.TokenCount
	} else {
		summary.TokenCount = tokenCounter(row.Summary)
	}
	return summary
}

// sessionItems loads the memories a session rollup is derived from: the transient row of this
// exact session, plus the user-scoped namespaces the session can see.
func (s *MemoryService) sessionItems(ctx context.Context, scope Scope) ([]model.MemoryItem, error) {
	var items []model.MemoryItem
	if err := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("namespace IN ?", scope.Namespaces()).
		Where("status = ?", model.ItemStatusActive).
		Where("expires_at IS NULL OR expires_at > ?", time.Now()).
		Order("importance DESC").
		Order("created_at DESC").
		Limit(maxSessionRollupItems).
		Find(&items).Error; err != nil {
		return nil, wrapErr(CodeInternal, "failed to load session memories", err)
	}
	return items, nil
}

// maxSessionRollupItems bounds how many memories feed the deterministic rollup.
const maxSessionRollupItems = 12

// buildSessionRollup renders the local, LLM-free session digest.
func buildSessionRollup(items []model.MemoryItem, maxTokens int) string {
	if len(items) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("This session so far (" + itoa(len(items)) + " memories):\n")
	for _, item := range items {
		line := strings.TrimSpace(item.Title)
		if line == "" {
			line = trimToTokens(strings.TrimSpace(item.Content), 40)
		}
		if line == "" {
			continue
		}
		builder.WriteString("- " + line + "\n")
	}

	tags := topTags(items, 8)
	if len(tags) > 0 {
		builder.WriteString("topics: " + strings.Join(tags, ", "))
	}

	text := strings.TrimSpace(builder.String())
	if maxTokens > 0 {
		text = trimToTokens(text, maxTokens)
	}
	return text
}

func topTags(items []model.MemoryItem, limit int) []string {
	counts := make(map[string]int)
	for _, item := range items {
		for _, tag := range parseTags(item.TagsJSON) {
			if tag = strings.TrimSpace(tag); tag != "" {
				counts[tag]++
			}
		}
	}
	if len(counts) == 0 {
		return nil
	}
	tags := make([]string, 0, len(counts))
	for tag := range counts {
		tags = append(tags, tag)
	}
	sort.Slice(tags, func(i, j int) bool {
		if counts[tags[i]] != counts[tags[j]] {
			return counts[tags[i]] > counts[tags[j]]
		}
		return tags[i] < tags[j]
	})
	if len(tags) > limit {
		tags = tags[:limit]
	}
	return tags
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
