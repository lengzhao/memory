package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
)

// RecallBudget describes how much room the recalled memories may take in a prompt. Without it,
// recall is limited only by TopK, which makes context assembly a guessing game for the host:
// eight long memories can blow a context window that eight short ones would fit into.
type RecallBudget struct {
	// MaxTokens caps the tokens spent on memory text.
	MaxTokens int
	// MaxItems caps the number of memories regardless of size.
	MaxItems int
	// PerItemMaxTokens truncates an individual memory; its full text stays in the store and can
	// still be fetched by ID.
	PerItemMaxTokens int
	// IncludeSummaryText prefers Summary over Content when a memory has one.
	IncludeSummaryText bool
	// IncludeNamespaceSummary appends the namespace digests of the namespaces that produced hits.
	IncludeNamespaceSummary bool
	// NamespaceSummaryMaxTokens caps the space the digests may take.
	NamespaceSummaryMaxTokens int
	// Formalizer turns the selected memories into the final text. Defaults to a compact listing.
	// It receives the rows in rank order.
	Formalizer func(ctx context.Context, hits []MemoryHit) string
}

// ContextBundle is a recall result already fitted to a token budget.
type ContextBundle struct {
	// Text is the assembled memory block, ready to be embedded in a prompt.
	Text string
	// Tokens is the token count of Text.
	Tokens int
	// Items lists every memory included in Text, with the text actually used.
	Items []BundleItem
	// NamespaceSummaries are the digests appended to Text (when requested).
	NamespaceSummaries []NamespaceDigest
	// Candidates is the number of memories considered before budgeting.
	Candidates int
	// Truncated reports whether the budget dropped or shortened memories.
	Truncated bool
}

// BundleItem is one memory as it appears in the context.
type BundleItem struct {
	ID         string
	Namespace  string
	Title      string
	Text       string
	Tokens     int
	Truncated  bool
	Score      float64
	SourceType model.SourceType
}

// RecallContext recalls memories, then fits them into the given budget. It is the entry point for
// hosts that assemble a prompt per turn: the memories come back pre-sized, and every omission is
// reported instead of silently dropped.
func (s *MemoryService) RecallContext(ctx context.Context, scope Scope, req RecallRequest, budget RecallBudget) (*ContextBundle, error) {
	recallReq := req
	// Fetch more candidates than needed so trimming has something to choose from.
	if budget.MaxItems > 0 {
		recallReq.TopK = budget.MaxItems * 3
	} else if budget.MaxTokens > 0 && recallReq.TopK <= 0 {
		recallReq.TopK = 30
	}

	hits, err := s.Recall(ctx, scope, recallReq)
	if err != nil {
		return nil, err
	}
	return s.buildBundle(ctx, scope, hits, budget)
}

// BuildContextMeta fits an existing recall result into a budget, without re-querying. Hosts that
// already have a ranked list (e.g. after reranking) use this.
func (s *MemoryService) BuildContextMeta(ctx context.Context, scope Scope, hits []MemoryHit, budget RecallBudget) (*ContextBundle, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	return s.buildBundle(ctx, scope, hits, budget)
}

func (s *MemoryService) buildBundle(ctx context.Context, scope Scope, hits []MemoryHit, budget RecallBudget) (*ContextBundle, error) {
	bundle := &ContextBundle{Candidates: len(hits), Items: make([]BundleItem, 0, len(hits))}

	// Budget accounting is done on the rendered form (bullet, separator, marker), not on the bare
	// memory text: what the model reads is what has to fit.
	used := 0
	if budget.MaxTokens > 0 && len(hits) > 0 {
		used = tokenCounter(bundleHeader)
	}
	for _, hit := range hits {
		if budget.MaxItems > 0 && len(bundle.Items) >= budget.MaxItems {
			bundle.Truncated = true
			break
		}

		text := memoryTextForBundle(hit, budget)
		if text == "" {
			continue
		}

		truncated := false
		if budget.PerItemMaxTokens > 0 {
			if trimmed := trimToTokens(text, budget.PerItemMaxTokens); trimmed != text {
				text = trimmed
				truncated = true
			}
		}

		if budget.MaxTokens > 0 {
			remaining := budget.MaxTokens - used
			if remaining <= 0 {
				bundle.Truncated = true
				break
			}
			// Trim to what is left, then charge the rendered cost of the result.
			if trimmed := trimToTokens(text, remaining); trimmed != text {
				text = trimmed
				truncated = true
			}
		}
		if strings.TrimSpace(text) == "" {
			bundle.Truncated = true
			continue
		}

		rendered := renderBundleItem(text, truncated)
		tokens := tokenCounter(rendered)
		if budget.MaxTokens > 0 && used+tokens > budget.MaxTokens {
			// The marker pushed it over: retry without claiming truncation.
			rendered = renderBundleItem(text, false)
			tokens = tokenCounter(rendered)
			truncated = false
		}
		if budget.MaxTokens > 0 && used+tokens > budget.MaxTokens {
			bundle.Truncated = true
			break
		}

		used += tokens
		bundle.Items = append(bundle.Items, BundleItem{
			ID:         hit.ID,
			Namespace:  hit.Namespace,
			Title:      hit.Title,
			Text:       text,
			Tokens:     tokens,
			Truncated:  truncated,
			Score:      hit.Score,
			SourceType: hit.SourceType,
		})
	}
	if len(bundle.Items) < len(hits) {
		bundle.Truncated = true
	}

	if budget.IncludeNamespaceSummary {
		digests, err := s.namespaceDigests(ctx, scope, bundle.Items, budget.NamespaceSummaryMaxTokens)
		if err != nil {
			return nil, err
		}
		bundle.NamespaceSummaries = digests
	}

	bundle.Text = s.formatBundle(ctx, bundle, budget)
	bundle.Tokens = tokenCounter(bundle.Text)
	return bundle, nil
}

// bundleHeader is the heading prepended to the memory list.
const bundleHeader = "## Memories\n"

// renderBundleItem is the exact text one bundled memory contributes to the prompt.
func renderBundleItem(text string, truncated bool) string {
	line := "- " + text + "\n"
	if truncated {
		line += "[truncated]\n"
	}
	return line
}

// memoryTextForBundle picks the text of one memory, preferring the summary when asked and the
// memory carries one.
func memoryTextForBundle(hit MemoryHit, budget RecallBudget) string {
	if budget.IncludeSummaryText && strings.TrimSpace(hit.Summary) != "" {
		if title := strings.TrimSpace(hit.Title); title != "" {
			return title + ": " + strings.TrimSpace(hit.Summary)
		}
		return strings.TrimSpace(hit.Summary)
	}

	parts := make([]string, 0, 2)
	if title := strings.TrimSpace(hit.Title); title != "" {
		parts = append(parts, title)
	}
	if content := strings.TrimSpace(hit.Content); content != "" {
		parts = append(parts, content)
	}
	switch len(parts) {
	case 0:
		return strings.TrimSpace(hit.Summary)
	case 1:
		return parts[0]
	default:
		return parts[0] + ": " + parts[1]
	}
}

func (s *MemoryService) formatBundle(ctx context.Context, bundle *ContextBundle, budget RecallBudget) string {
	if budget.Formalizer != nil {
		hits := make([]MemoryHit, 0, len(bundle.Items))
		for _, item := range bundle.Items {
			hits = append(hits, MemoryHit{MemoryItem: model.MemoryItem{
				ID: item.ID, Namespace: item.Namespace, Title: item.Title, Content: item.Text,
			}, Score: item.Score})
		}
		return budget.Formalizer(ctx, hits)
	}

	var builder strings.Builder
	if len(bundle.Items) > 0 {
		builder.WriteString(bundleHeader)
		for _, item := range bundle.Items {
			builder.WriteString(renderBundleItem(item.Text, item.Truncated))
		}
	}
	for _, digest := range bundle.NamespaceSummaries {
		builder.WriteString("\n")
		builder.WriteString(renderDigest(digest.Title, digest.Summary))
	}
	return strings.TrimSpace(builder.String())
}

// ---- namespace summaries --------------------------------------------------------------------

// summaryStaleAfter is how long a namespace digest is reused before being recomputed.
const summaryStaleAfter = 10 * time.Minute

// NamespaceDigest is the rolling digest of one namespace.
type NamespaceDigest struct {
	Namespace string
	Title     string
	Summary   string
	ItemCount int
	Tokens    int
	Stale     bool
}

// maxNamespaceDigestItems bounds how many memories feed one digest.
const maxNamespaceDigestItems = 20

// renderDigest is the exact text a namespace digest contributes to the prompt.
func renderDigest(title, summary string) string {
	var builder strings.Builder
	builder.WriteString("## ")
	builder.WriteString(title)
	builder.WriteString("\n")
	builder.WriteString(summary)
	builder.WriteString("\n")
	return builder.String()
}

// namespaceDigests returns the digests covering the namespaces of the bundled memories. Digests are
// cached in namespace_summaries and refreshed when the namespace has changed or the cache is old.
func (s *MemoryService) namespaceDigests(ctx context.Context, scope Scope, items []BundleItem, maxTokens int) ([]NamespaceDigest, error) {
	namespaces := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for _, item := range items {
		if _, ok := seen[item.Namespace]; ok {
			continue
		}
		seen[item.Namespace] = struct{}{}
		namespaces = append(namespaces, item.Namespace)
	}
	if len(namespaces) == 0 {
		return nil, nil
	}
	sort.Strings(namespaces)

	digests := make([]NamespaceDigest, 0, len(namespaces))
	used := 0
	for _, namespace := range namespaces {
		digest, err := s.RefreshNamespaceSummary(ctx, scope, namespace)
		if err != nil {
			return nil, err
		}
		if digest.Summary == "" {
			continue
		}
		titleCost := tokenCounter(renderDigest(digest.Title, ""))
		tokens := titleCost + tokenCounter(digest.Summary)
		if maxTokens > 0 {
			if used+tokens > maxTokens {
				remaining := maxTokens - used - titleCost
				if remaining <= 0 {
					break
				}
				digest.Summary = trimToTokens(digest.Summary, remaining)
				tokens = titleCost + tokenCounter(digest.Summary)
			}
			if used+tokens > maxTokens {
				break
			}
		}
		digest.Tokens = tokens
		used += tokens
		digests = append(digests, digest)
	}
	return digests, nil
}

// RefreshNamespaceSummary regenerates (or reuses) the digest of one namespace.
//
// The digest is derived from the namespace's active memories and cached together with a content
// fingerprint: the policy fields SummaryEnabled / SummaryItemTokenThreshold finally have an effect,
// and a host that assembles prompts per turn does not re-read the namespace every time.
func (s *MemoryService) RefreshNamespaceSummary(ctx context.Context, scope Scope, namespace string) (NamespaceDigest, error) {
	if err := scope.Validate(); err != nil {
		return NamespaceDigest{}, err
	}
	if !scope.Owns(namespace) {
		return NamespaceDigest{}, newErr(CodeUnauthorized, "namespace does not belong to this scope: "+namespace)
	}

	nsType := inferScopeNamespaceType(namespace)
	policy, err := s.policyManager.GetPolicy(ctx, namespace)
	if err != nil {
		return NamespaceDigest{}, err
	}

	var items []model.MemoryItem
	if err := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("namespace = ? AND status = ?", namespace, model.ItemStatusActive).
		Where("expires_at IS NULL OR expires_at > ?", time.Now()).
		Order("importance DESC").
		Order("created_at DESC").
		Limit(maxNamespaceDigestItems).
		Find(&items).Error; err != nil {
		return NamespaceDigest{}, wrapErr(CodeInternal, "failed to load namespace memories", err)
	}

	var total int64
	if err := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("namespace = ? AND status = ?", namespace, model.ItemStatusActive).
		Count(&total).Error; err != nil {
		return NamespaceDigest{}, wrapErr(CodeInternal, "failed to count namespace memories", err)
	}

	digest := NamespaceDigest{
		Namespace: namespace,
		Title:     "Namespace: " + namespace,
		ItemCount: int(total),
	}
	if nsType != "" {
		digest.Title = "Namespace: " + namespace + " (" + string(nsType) + ")"
	}
	if !policy.SummaryEnabled || len(items) == 0 {
		return digest, nil
	}

	fingerprint := namespaceFingerprint(items, total)
	cached, ok, err := s.loadCachedSummary(ctx, namespace)
	if err != nil {
		return NamespaceDigest{}, err
	}
	if ok && cached.Fingerprint == fingerprint && time.Since(cached.UpdatedAt) < summaryStaleAfter {
		digest.Summary = cached.Summary
		digest.Stale = false
		return digest, nil
	}

	digest.Summary = buildNamespaceDigest(items, int(total), policy.SummaryItemTokenThreshold)
	digest.Stale = true
	if err := s.storeCachedSummary(ctx, namespace, digest.Summary, fingerprint, int(total)); err != nil {
		// A cache write failure must not fail the recall: the digest above is already correct.
		GetLogger().Warn("failed to cache namespace summary", "namespace", namespace, "error", err)
	}
	return digest, nil
}

// GenerateNamespaceSummary is the explicit variant of RefreshNamespaceSummary: it always
// recomputes and returns the digest text.
func (s *MemoryService) GenerateNamespaceSummary(ctx context.Context, scope Scope, namespace string) (string, error) {
	if err := scope.Validate(); err != nil {
		return "", err
	}
	if !scope.Owns(namespace) {
		return "", newErr(CodeUnauthorized, "namespace does not belong to this scope: "+namespace)
	}

	digest, err := s.RefreshNamespaceSummary(ctx, scope, namespace)
	if err != nil {
		return "", err
	}
	if digest.Summary == "" {
		return "", wrapErr(CodeNotFound, "no summarisable memories in namespace "+namespace, nil)
	}
	if !digest.Stale {
		// Force a recomputation when the caller asked explicitly.
		items, err := s.namespaceItems(ctx, namespace)
		if err != nil {
			return "", err
		}
		policy, err := s.policyManager.GetPolicy(ctx, namespace)
		if err != nil {
			return "", err
		}
		digest.Summary = buildNamespaceDigest(items, digest.ItemCount, policy.SummaryItemTokenThreshold)
		if err := s.storeCachedSummary(ctx, namespace, digest.Summary, namespaceFingerprint(items, int64(digest.ItemCount)), digest.ItemCount); err != nil {
			return "", err
		}
	}
	return digest.Summary, nil
}

func (s *MemoryService) namespaceItems(ctx context.Context, namespace string) ([]model.MemoryItem, error) {
	var items []model.MemoryItem
	if err := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("namespace = ? AND status = ?", namespace, model.ItemStatusActive).
		Where("expires_at IS NULL OR expires_at > ?", time.Now()).
		Order("importance DESC").
		Order("created_at DESC").
		Limit(maxNamespaceDigestItems).
		Find(&items).Error; err != nil {
		return nil, wrapErr(CodeInternal, "failed to load namespace memories", err)
	}
	return items, nil
}

type cachedSummary struct {
	Summary     string
	Fingerprint string
	UpdatedAt   time.Time
}

func (s *MemoryService) loadCachedSummary(ctx context.Context, namespace string) (cachedSummary, bool, error) {
	var row model.NamespaceSummary
	err := s.db.WithContext(ctx).Where("namespace = ?", namespace).First(&row).Error
	switch {
	case err == nil:
		fingerprint := ""
		if row.Fingerprint != nil {
			fingerprint = *row.Fingerprint
		}
		return cachedSummary{Summary: row.Summary, Fingerprint: fingerprint, UpdatedAt: row.UpdatedAt}, true, nil
	case err == gorm.ErrRecordNotFound:
		return cachedSummary{}, false, nil
	default:
		return cachedSummary{}, false, wrapErr(CodeInternal, "failed to load namespace summary", err)
	}
}

func (s *MemoryService) storeCachedSummary(ctx context.Context, namespace, summary, fingerprint string, itemCount int) error {
	now := time.Now()
	row := model.NamespaceSummary{
		ID:          model.GenerateID(),
		Namespace:   namespace,
		Summary:     summary,
		ItemCount:   itemCount,
		Fingerprint: &fingerprint,
		UpdatedAt:   now,
	}
	var existing model.NamespaceSummary
	err := s.db.WithContext(ctx).Where("namespace = ?", namespace).First(&existing).Error
	switch {
	case err == gorm.ErrRecordNotFound:
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return wrapErr(CodeInternal, "failed to store namespace summary", err)
		}
	case err != nil:
		return wrapErr(CodeInternal, "failed to load namespace summary", err)
	default:
		if err := s.db.WithContext(ctx).Model(&model.NamespaceSummary{}).
			Where("namespace = ?", namespace).
			Updates(map[string]interface{}{
				"summary":     summary,
				"item_count":  itemCount,
				"fingerprint": fingerprint,
				"updated_at":  now,
			}).Error; err != nil {
			return wrapErr(CodeInternal, "failed to update namespace summary", err)
		}
	}
	return nil
}

// namespaceFingerprint identifies the content a digest was built from, so a cached digest is
// reused only while the namespace is unchanged.
func namespaceFingerprint(items []model.MemoryItem, total int64) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(strings.Join([]string{}, "")))
	writeInt(&builder, total)
	for _, item := range items {
		builder.WriteString(item.ID)
		builder.WriteString(item.UpdatedAt.UTC().Format(time.RFC3339Nano))
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:16])
}

// buildNamespaceDigest renders the namespace overview: how much is there, what dominates it, and a
// few of the most important lines. It is deterministic (no LLM call on the recall path) and bounded
// by the policy's per-item token threshold.
func buildNamespaceDigest(items []model.MemoryItem, total int, itemTokenThreshold int) string {
	if len(items) == 0 {
		return ""
	}
	if itemTokenThreshold <= 0 {
		itemTokenThreshold = 200
	}

	var builder strings.Builder
	visible := len(items)
	if total > visible {
		builder.WriteString(itoa(visible) + " of " + itoa(total) + " memories, most important first:\n")
	} else {
		builder.WriteString(itoa(total) + " memories, most important first:\n")
	}

	// Tag frequency gives the digest something topic-level to say without an LLM.
	tagCounts := make(map[string]int)
	for _, item := range items {
		for _, tag := range parseTags(item.TagsJSON) {
			if tag = strings.TrimSpace(tag); tag != "" {
				tagCounts[tag]++
			}
		}
	}

	for _, item := range items {
		line := strings.TrimSpace(item.Title)
		if line == "" {
			line = trimToTokens(strings.TrimSpace(item.Content), itemTokenThreshold)
		} else if content := strings.TrimSpace(item.Content); content != "" {
			line += ": " + trimToTokens(content, itemTokenThreshold)
		}
		if line == "" {
			continue
		}
		builder.WriteString("- ")
		builder.WriteString(line)
		builder.WriteString("\n")
	}

	if len(tagCounts) > 0 {
		tags := make([]string, 0, len(tagCounts))
		for tag := range tagCounts {
			tags = append(tags, tag)
		}
		sort.Slice(tags, func(i, j int) bool {
			if tagCounts[tags[i]] != tagCounts[tags[j]] {
				return tagCounts[tags[i]] > tagCounts[tags[j]]
			}
			return tags[i] < tags[j]
		})
		if len(tags) > 8 {
			tags = tags[:8]
		}
		builder.WriteString("topics: " + strings.Join(tags, ", "))
	}

	return strings.TrimSpace(builder.String())
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := make([]byte, 0, 12)
	negative := v < 0
	if negative {
		v = -v
	}
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

func writeInt(builder *strings.Builder, v int64) {
	builder.WriteString(itoa(int(v)))
	builder.WriteString("|")
}
