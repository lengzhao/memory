// Package service provides the core memory service implementation.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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

	// ApplyPolicies makes Remember derive expires_at from the namespace TTL policy
	// (TTLSeconds/TTLPolicy/SlidingTTLThreshold) when the request sets no explicit TTL.
	// Off by default, which preserves "no TTL passed -> never expires".
	ApplyPolicies bool

	// RecordEvents writes memory_events audit rows for write operations.
	// Off by default (the table grows), actor/trace ids come from WithActor/WithTrace.
	RecordEvents bool
}

// MemoryService provides core memory operations.
type MemoryService struct {
	db             *gorm.DB
	config         Config
	accessStatsSem chan struct{}
	policyManager  *PolicyManager
}

// NewMemoryService creates a new memory service instance.
func NewMemoryService(db *gorm.DB) *MemoryService {
	return &MemoryService{
		db:             db,
		accessStatsSem: make(chan struct{}, 8),
		policyManager:  NewPolicyManager(db),
	}
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
	// Namespace is only honoured in global mode (AllowGlobal). Inside an isolation context
	// the namespace is derived from the identity, and passing one is rejected.
	Namespace  string
	Title      string
	Content    string
	Summary    string
	Tags       []string
	SourceType model.SourceType
	SourceRef  string
	Importance int
	Confidence float64
	TTLSeconds *int
	DedupeKey  *string
}

// MaxContentRunes bounds the size of a single memory row.
const MaxContentRunes = 100_000

// Remember stores a memory item with idempotency support.
//
// Isolation: either ctx carries a complete isolation identity (WithIsolation) and the
// namespace is derived from it, or ctx was marked with AllowGlobal and the caller may pass
// an explicit Namespace. Everything else fails closed.
//
// Validation: Importance must be 0-100 and Confidence 0.0-1.0; out-of-range values are
// rejected instead of silently skewing recall ranking.
func (s *MemoryService) Remember(ctx context.Context, req RememberRequest) (string, error) {
	if req.NamespaceType == "" {
		req.NamespaceType = model.NamespaceTypeTransient
	}
	if err := req.validate(); err != nil {
		return "", err
	}

	meta, isolated, err := resolveIsolation(ctx)
	if err != nil {
		return "", err
	}

	namespace, err := resolveTargetNamespace(meta, isolated, req.Namespace, req.NamespaceType)
	if err != nil {
		return "", err
	}

	// Dedupe keys are scoped to the uniqueness domain of the namespace: session-scoped for
	// transient rows, user-scoped for profile/action/knowledge rows. Prefixing every key with
	// the session (historic behaviour) made cross-session idempotency impossible for facts
	// shared across sessions.
	if req.DedupeKey != nil {
		trimmed := strings.TrimSpace(*req.DedupeKey)
		if trimmed == "" {
			req.DedupeKey = nil
		} else {
			scoped := buildDedupeKey(meta, req.NamespaceType, trimmed, isolated)
			req.DedupeKey = &scoped
		}
	}

	now := time.Now()

	// Check for duplicate by dedupe_key if provided
	if req.DedupeKey != nil {
		var existing model.MemoryItem
		err := s.db.WithContext(ctx).
			Where("namespace = ? AND dedupe_key = ?", namespace, *req.DedupeKey).
			First(&existing).Error
		if err == nil {
			// Duplicate found, return existing ID
			return existing.ID, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return "", wrapErr(CodeInternal, "failed to check dedupe key", err)
		}
	}

	// Calculate expires_at: an explicit TTL wins, otherwise the namespace policy applies
	// when Config.ApplyPolicies is enabled (nil for manual/no-expiry policies).
	expiresAt, err := s.resolveExpiresAt(ctx, req.TTLSeconds, namespace, now)
	if err != nil {
		return "", err
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

	if err := createRow(ctx, s.db, &item); err != nil {
		if req.DedupeKey != nil && isUniqueConstraintError(err) {
			var existing model.MemoryItem
			qErr := s.db.WithContext(ctx).
				Where("namespace = ? AND dedupe_key = ?", namespace, *req.DedupeKey).
				First(&existing).Error
			if qErr == nil {
				return existing.ID, nil
			}
			return "", wrapErr(CodeInternal, "dedupe conflict but failed to load existing item", qErr)
		}
		return "", wrapErr(CodeInternal, "failed to create memory", err)
	}

	s.recordEvent(ctx, model.EventTypeCreate, &item.ID, namespace, map[string]interface{}{
		"source_type": string(req.SourceType),
		"source_ref":  req.SourceRef,
	})

	// Trigger callback
	if s.config.OnCreated != nil {
		s.config.OnCreated(ctx, item)
	}

	return item.ID, nil
}

// validate rejects values that would corrupt ranking or search.
func (r RememberRequest) validate() error {
	switch r.NamespaceType {
	case model.NamespaceTypeTransient, model.NamespaceTypeProfile, model.NamespaceTypeAction, model.NamespaceTypeKnowledge:
	default:
		return newErr(CodeValidation, fmt.Sprintf("invalid namespace_type %q (want transient|profile|action|knowledge)", r.NamespaceType))
	}
	if strings.TrimSpace(r.Content) == "" && strings.TrimSpace(r.Title) == "" {
		return newErr(CodeValidation, "content must not be empty")
	}
	if len([]rune(r.Content)) > MaxContentRunes {
		return newErr(CodeValidation, fmt.Sprintf("content exceeds %d runes", MaxContentRunes))
	}
	if r.Importance < 0 || r.Importance > 100 {
		return newErr(CodeValidation, fmt.Sprintf("importance must be within 0-100, got %d", r.Importance))
	}
	if r.Confidence < 0 || r.Confidence > 1 {
		return newErr(CodeValidation, fmt.Sprintf("confidence must be within 0.0-1.0, got %v", r.Confidence))
	}
	if r.TTLSeconds != nil && *r.TTLSeconds < 0 {
		return newErr(CodeValidation, "ttl_seconds must not be negative")
	}
	return nil
}

// resolveTargetNamespace maps (request namespace, namespace type) to a concrete namespace,
// enforcing isolation.
func resolveTargetNamespace(meta IsolationMeta, isolated bool, namespace string, nsType model.NamespaceType) (string, error) {
	if isolated {
		if strings.TrimSpace(namespace) != "" {
			return "", newErr(CodeValidation, "namespace must not be provided when context isolation is enabled")
		}
		return buildNamespace(meta, nsType), nil
	}
	if strings.TrimSpace(namespace) != "" {
		return strings.TrimSpace(namespace), nil
	}
	return buildDefaultNamespace(nsType), nil
}

// buildDedupeKey scopes a caller-supplied dedupe key to the namespace's uniqueness domain.
func buildDedupeKey(meta IsolationMeta, nsType model.NamespaceType, key string, isolated bool) string {
	if !isolated || !meta.any() {
		return key
	}
	switch nsType {
	case model.NamespaceTypeTransient:
		return strings.Join([]string{meta.TenantID, meta.UserID, meta.SessionID, meta.AgentID, key}, ":")
	default:
		return strings.Join([]string{meta.TenantID, meta.UserID, key}, ":")
	}
}

// resolveExpiresAt returns the expiry timestamp for a new row.
func (s *MemoryService) resolveExpiresAt(ctx context.Context, ttlSeconds *int, namespace string, now time.Time) (*time.Time, error) {
	if ttlSeconds != nil {
		if *ttlSeconds <= 0 {
			return nil, nil
		}
		t := now.Add(time.Duration(*ttlSeconds) * time.Second)
		return &t, nil
	}
	if !s.config.ApplyPolicies {
		return nil, nil
	}
	policy, err := s.policyManager.GetPolicy(ctx, namespace)
	if err != nil {
		return nil, err
	}
	if policy.TTLSeconds == nil || *policy.TTLSeconds <= 0 {
		return nil, nil
	}
	t := now.Add(time.Duration(*policy.TTLSeconds) * time.Second)
	return &t, nil
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
	// InstanceIDs restricts the recall to specific memory items.
	InstanceIDs []string
	// MinConfidence filters on stored confidence. DefaultMinConfidence applies when unset
	// so rows written without a confidence stay recallable; set SkipConfidenceFilter to
	// disable the filter entirely (previously impossible: 0 was silently rewritten to 0.5).
	MinConfidence        float64
	SkipConfidenceFilter bool
	MinImportance        int
	TopK                 int
	ExcludeItemIDs       []string
}

// DefaultMinConfidence is applied when a recall/list request leaves MinConfidence unset.
const DefaultMinConfidence = 0.5

// ListRequest represents a request to list memories by time.
type ListRequest struct {
	Namespaces     []string
	NamespaceTypes []model.NamespaceType
	TagsAny        []string
	TagsAll        []string
	TimeRangeStart *time.Time
	TimeRangeEnd   *time.Time
	IncludeExpired bool
	InstanceIDs    []string
	MinConfidence  float64
	// SkipConfidenceFilter disables the confidence filter (MinConfidence=0 would
	// otherwise fall back to DefaultMinConfidence).
	SkipConfidenceFilter bool
	MinImportance        int
	TopK                 int
	Offset               int
	ExcludeItemIDs       []string
	Order                string // "desc" (default) or "asc", based on created_at
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
//
// Candidates come from (a) the tokenized FTS index, (b) a LIKE fallback when that index
// returns nothing (CJK robustness), or (c) all rows when no query was given. Candidates are
// ranked by the namespace policy weights, where the text component uses the BM25 relevance
// of each match instead of a constant.
func (s *MemoryService) Recall(ctx context.Context, req RecallRequest) ([]MemoryHit, error) {
	meta, isolated, err := resolveIsolation(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.applyNamespaceScope(isolated, meta, &req.Namespaces, req.NamespaceTypes); err != nil {
		return nil, err
	}

	if req.TopK <= 0 {
		req.TopK = 10
	}
	req.MinConfidence = resolveMinConfidence(req.MinConfidence, req.SkipConfidenceFilter)

	query := s.baseFilteredQuery(ctx, filterOptions{
		Namespaces:     req.Namespaces,
		NamespaceTypes: req.NamespaceTypes,
		TagsAny:        req.TagsAny,
		TagsAll:        req.TagsAll,
		TimeRangeStart: req.TimeRangeStart,
		TimeRangeEnd:   req.TimeRangeEnd,
		IncludeExpired: req.IncludeExpired,
		ExcludeItemIDs: req.ExcludeItemIDs,
		InstanceIDs:    req.InstanceIDs,
		MinConfidence:  req.MinConfidence,
		MinImportance:  req.MinImportance,
	})

	// Text search if query provided
	var itemIDs []string
	var ftsScores map[string]float64
	var useLike bool
	if req.Query != "" {
		candidateLimit := req.TopK * 3
		if candidateLimit < 50 {
			candidateLimit = 50
		}
		itemIDs, ftsScores, useLike = s.searchText(ctx, req.Query, candidateLimit)
		if useLike {
			// LIKE fallback over the raw columns, with wildcards escaped so user input
			// cannot smuggle LIKE metacharacters.
			pattern := "%" + escapeLikePattern(req.Query) + "%"
			query = query.Where(
				"(title LIKE ? ESCAPE '\\' OR content LIKE ? ESCAPE '\\' OR summary LIKE ? ESCAPE '\\')",
				pattern, pattern, pattern,
			)
		} else {
			// FTS results (already ranked by BM25 relevance)
			if len(itemIDs) == 0 {
				return []MemoryHit{}, nil
			}
			query = query.Where("id IN ?", itemIDs)
		}
	}

	// Execute query
	var items []model.MemoryItem
	if err := query.Limit(req.TopK * 2).Find(&items).Error; err != nil {
		return nil, wrapErr(CodeInternal, "query failed", err)
	}

	// Score and rank results
	hits, err := s.scoreAndRank(ctx, items, req, ftsScores)
	if err != nil {
		return nil, err
	}

	// Limit to TopK
	if len(hits) > req.TopK {
		hits = hits[:req.TopK]
	}

	// Update access stats (async is fine, don't block return)
	if len(itemIDs) == 0 && len(hits) > 0 {
		itemIDs = make([]string, len(hits))
		for i, h := range hits {
			itemIDs[i] = h.ID
		}
	}
	s.enqueueAccessStats(itemIDs)

	return hits, nil
}

// applyNamespaceScope rewrites a request's namespace list according to the caller's identity.
// Inside an isolation context the readable namespaces are derived from the identity (an
// explicit list is rejected, which prevents cross-tenant reads).
func (s *MemoryService) applyNamespaceScope(isolated bool, meta IsolationMeta, namespaces *[]string, nsTypes []model.NamespaceType) error {
	if !isolated {
		return nil
	}
	if len(*namespaces) > 0 {
		return newErr(CodeValidation, "namespaces must not be provided when context isolation is enabled")
	}
	*namespaces = buildAllowedNamespaces(meta, nsTypes)
	return nil
}

// resolveMinConfidence applies DefaultMinConfidence when the request leaves it unset.
func resolveMinConfidence(requested float64, skip bool) float64 {
	if skip || requested < 0 {
		return 0
	}
	if requested == 0 {
		return DefaultMinConfidence
	}
	return requested
}

// List returns memories ordered by created_at.
func (s *MemoryService) List(ctx context.Context, req ListRequest) ([]model.MemoryItem, error) {
	meta, isolated, err := resolveIsolation(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.applyNamespaceScope(isolated, meta, &req.Namespaces, req.NamespaceTypes); err != nil {
		return nil, err
	}

	if req.TopK <= 0 {
		req.TopK = 10
	}
	req.MinConfidence = resolveMinConfidence(req.MinConfidence, req.SkipConfidenceFilter)

	query := s.baseFilteredQuery(ctx, filterOptions{
		Namespaces:     req.Namespaces,
		NamespaceTypes: req.NamespaceTypes,
		TagsAny:        req.TagsAny,
		TagsAll:        req.TagsAll,
		TimeRangeStart: req.TimeRangeStart,
		TimeRangeEnd:   req.TimeRangeEnd,
		IncludeExpired: req.IncludeExpired,
		ExcludeItemIDs: req.ExcludeItemIDs,
		InstanceIDs:    req.InstanceIDs,
		MinConfidence:  req.MinConfidence,
		MinImportance:  req.MinImportance,
	})

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
func (s *MemoryService) scoreAndRank(ctx context.Context, items []model.MemoryItem, req RecallRequest, ftsScores map[string]float64) ([]MemoryHit, error) {
	now := time.Now()
	hits := make([]MemoryHit, 0, len(items))
	pm := s.policyManager
	weightCache := make(map[string]rankWeights)

	for _, item := range items {
		hit := MemoryHit{MemoryItem: item}

		// Text relevance: BM25-derived when the row came from the FTS index, otherwise a
		// neutral score (LIKE fallback / unfiltered listing).
		if req.Query != "" {
			if score, ok := ftsScores[item.ID]; ok {
				hit.FTSScore = score
				hit.MatchReasons = append(hit.MatchReasons, "fts_match")
			} else {
				hit.FTSScore = 0.5
				hit.MatchReasons = append(hit.MatchReasons, "substring_match")
			}
		}

		// Recency score (exponential decay, 7 days half-life)
		hoursAgo := now.Sub(item.CreatedAt).Hours()
		hit.RecencyScore = expDecay(hoursAgo, 168)

		// Importance score (normalized)
		hit.ImportanceScore = float64(item.Importance) / 100.0

		// Confidence score
		hit.ConfidenceScore = item.Confidence

		// Access boost
		accessBoost := min(0.1, float64(item.AccessCount)/100.0)

		weights, err := rankWeightsForNamespace(ctx, pm, item.Namespace, weightCache)
		if err != nil {
			return nil, err
		}
		// Weighted score, clamped so that no component (or the access boost) can push the
		// score outside the [0,1] scale used for ordering.
		hit.Score = normalizeScore(weights.FTS*hit.FTSScore +
			weights.Recency*hit.RecencyScore +
			weights.Importance*hit.ImportanceScore +
			weights.Confidence*hit.ConfidenceScore + accessBoost)

		hits = append(hits, hit)
	}

	// Sort by score descending; ties break on recency then id for a stable order.
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if !hits[i].CreatedAt.Equal(hits[j].CreatedAt) {
			return hits[i].CreatedAt.After(hits[j].CreatedAt)
		}
		return hits[i].ID < hits[j].ID
	})

	return hits, nil
}

type rankWeights struct {
	FTS        float64
	Recency    float64
	Importance float64
	Confidence float64
}

func rankWeightsForNamespace(
	ctx context.Context,
	pm *PolicyManager,
	namespace string,
	cache map[string]rankWeights,
) (rankWeights, error) {
	if w, ok := cache[namespace]; ok {
		return w, nil
	}
	policy, err := pm.GetPolicy(ctx, namespace)
	if err != nil {
		return rankWeights{}, wrapErr(CodeInternal, "load namespace policy failed", err)
	}
	fts, recency, importance, confidence := pm.GetRankWeights(policy)
	w := rankWeights{
		FTS:        fts,
		Recency:    recency,
		Importance: importance,
		Confidence: confidence,
	}
	sum := w.FTS + w.Recency + w.Importance + w.Confidence
	if sum <= 0 {
		w = rankWeights{FTS: 0.55, Recency: 0.20, Importance: 0.15, Confidence: 0.10}
	} else if math.Abs(sum-1.0) > 0.0001 {
		w.FTS = w.FTS / sum
		w.Recency = w.Recency / sum
		w.Importance = w.Importance / sum
		w.Confidence = w.Confidence / sum
	}
	cache[namespace] = w
	return w, nil
}

func expDecay(hoursAgo, halfLifeHours float64) float64 {
	if halfLifeHours <= 0 {
		return 0
	}
	if hoursAgo < 0 {
		return 1.0
	}
	// Exponential decay with the given half-life: 0.5 ** (hoursAgo / halfLife).
	return math.Pow(0.5, hoursAgo/halfLifeHours)
}

// normalizeScore clamps a score into [0,1] and drops NaN values.
func normalizeScore(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// searchText performs text search using the pre-tokenized FTS5 index.
// The query is tokenized with jiebago for CJK support. It returns the candidate item IDs,
// their BM25-derived similarity, and whether the LIKE fallback must be used instead.
func (s *MemoryService) searchText(ctx context.Context, query string, limit int) ([]string, map[string]float64, bool) {
	// Tokenize the query (handles both CJK and English)
	tokenizedQuery := TokenizeQuery(query)
	if tokenizedQuery == "" {
		// No valid tokens, try LIKE fallback
		return nil, nil, true
	}

	itemIDs, scores := s.searchFTS(ctx, tokenizedQuery, limit)
	if len(itemIDs) == 0 {
		// FTS found nothing, try LIKE fallback
		return nil, nil, true
	}
	return itemIDs, scores, false
}

// searchFTS performs an FTS5 search over the pre-tokenized index and returns the matches
// ordered by relevance (BM25, lower is better).
//
// Tokens are OR'ed for recall, but a hit must contain at least one whole query token: the
// jiebago search-mode index emits overlapping tokens ("数据" inside "数据库"), so raw OR
// matching alone is far too loose.
func (s *MemoryService) searchFTS(ctx context.Context, tokenizedQuery string, limit int) ([]string, map[string]float64) {
	tokens := strings.Fields(tokenizedQuery)
	if len(tokens) == 0 {
		return nil, nil
	}

	// Build an OR query over quoted tokens. Quotes are stripped from the tokens so user input
	// cannot rewrite the FTS query syntax.
	var orQuery strings.Builder
	for _, token := range tokens {
		token = strings.ReplaceAll(token, "\"", "")
		if token == "" {
			continue
		}
		if orQuery.Len() > 0 {
			orQuery.WriteString(" OR ")
		}
		orQuery.WriteString("\"" + token + "\"")
	}
	if orQuery.Len() == 0 {
		return nil, nil
	}

	var results []struct {
		ItemID        string
		TokenizedText string
		BM25          float64
	}
	err := s.db.WithContext(ctx).Raw(`
		SELECT item_id,
		       tokenized_content AS tokenized_text,
		       bm25(fts_memory) AS bm25
		FROM fts_memory
		WHERE tokenized_content MATCH ?
		ORDER BY bm25
		LIMIT ?
	`, orQuery.String(), limit).Scan(&results).Error
	if err != nil {
		GetLogger().Warn("fts search failed, falling back to LIKE", "error", err)
		return nil, nil
	}

	itemIDs := make([]string, 0, len(results))
	scores := make(map[string]float64, len(results))
	for _, r := range results {
		if !containsFullToken(r.TokenizedText, tokens) {
			continue
		}
		itemIDs = append(itemIDs, r.ItemID)
		scores[r.ItemID] = bm25ToSimilarity(r.BM25)
	}
	return itemIDs, scores
}

// containsFullToken reports whether the indexed token stream contains at least one of the
// query tokens as a whole token (fields are space-delimited, so "数据库" does not match
// "数据库连接").
func containsFullToken(tokenizedText string, tokens []string) bool {
	indexed := make(map[string]struct{})
	for _, t := range strings.Fields(tokenizedText) {
		indexed[t] = struct{}{}
	}
	for _, t := range tokens {
		if _, ok := indexed[t]; ok {
			return true
		}
	}
	return false
}

// escapeLikePattern escapes LIKE metacharacters so user input is matched literally.
func escapeLikePattern(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "%", "\\%")
	s = strings.ReplaceAll(s, "_", "\\_")
	return s
}

// updateAccessStats updates access count and last_access_at for items.
func (s *MemoryService) updateAccessStats(ctx context.Context, itemIDs []string) {
	if len(itemIDs) == 0 {
		return
	}
	now := time.Now()
	result := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("id IN ?", itemIDs).
		Updates(map[string]interface{}{
			"access_count":   gorm.Expr("access_count + 1"),
			"last_access_at": now,
		})
	if result.Error != nil {
		GetLogger().Warn("update access stats failed", "error", result.Error, "item_count", len(itemIDs))
	}
}

func (s *MemoryService) enqueueAccessStats(itemIDs []string) {
	if len(itemIDs) == 0 {
		return
	}
	select {
	case s.accessStatsSem <- struct{}{}:
		go func(ids []string) {
			defer func() { <-s.accessStatsSem }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s.updateAccessStats(ctx, ids)
		}(append([]string(nil), itemIDs...))
	default:
		GetLogger().Warn("skip access stats update: worker queue full", "item_count", len(itemIDs))
	}
}

// ForgetRequest describes what to forget.
//
// Inside an isolation context the target is NamespaceType, or — when NamespaceType is empty —
// every namespace readable by the caller. It is deliberately NOT narrowed to transient: a
// Forget without a type previously fell back to the transient namespace and reported partial
// deletes as success, which silently kept profile/knowledge rows alive.
type ForgetRequest struct {
	ItemIDs       []string
	Namespace     string
	NamespaceType model.NamespaceType
	Mode          string // "soft" (default), "expire", "hard"
	Reason        string
	// BackupBeforeHardDelete keeps a copy of hard-deleted rows in deleted_items so they can
	// be inspected/restored until their retention window expires. Defaults to true.
	// Set it to false (or call PurgeExpiredDeleted later) for GDPR-style erasure.
	BackupBeforeHardDelete *bool
}

// Forget removes or marks memories as deleted/expired.
//
// The returned count is the number of rows actually affected; a request that matches nothing
// returns CodeNotFound instead of a silent zero when specific ItemIDs were requested.
func (s *MemoryService) Forget(ctx context.Context, req ForgetRequest) (int, error) {
	meta, isolated, err := resolveIsolation(ctx)
	if err != nil {
		return 0, err
	}

	targets := make([]string, 0, 1)
	if isolated {
		if strings.TrimSpace(req.Namespace) != "" {
			return 0, newErr(CodeValidation, "namespace must not be provided when context isolation is enabled")
		}
		if req.NamespaceType != "" {
			targets = append(targets, buildNamespace(meta, req.NamespaceType))
		} else {
			targets = buildAllowedNamespaces(meta, nil)
		}
	} else if ns := strings.TrimSpace(req.Namespace); ns != "" {
		targets = append(targets, ns)
	} else if req.NamespaceType != "" {
		targets = append(targets, buildDefaultNamespace(req.NamespaceType))
	}

	if req.Mode == "" {
		req.Mode = "soft"
	}

	scope := func(query *gorm.DB) *gorm.DB {
		if len(req.ItemIDs) > 0 {
			query = query.Where("id IN ?", req.ItemIDs)
		}
		if len(targets) == 1 {
			query = query.Where("namespace = ?", targets[0])
		} else if len(targets) > 1 {
			query = query.Where("namespace IN ?", targets)
		}
		if req.NamespaceType != "" {
			query = query.Where("namespace_type = ?", req.NamespaceType)
		}
		return query
	}

	// Collect the affected IDs up front: they drive the callbacks and the FTS index cleanup
	// (status changes are not covered by the DELETE trigger).
	var affected []string
	if err := scope(s.db.WithContext(ctx).Model(&model.MemoryItem{}).Select("id").
		Where("status = ?", model.ItemStatusActive)).Pluck("id", &affected).Error; err != nil {
		return 0, wrapErr(CodeInternal, "failed to resolve forget targets", err)
	}

	var rowsAffected int64
	switch req.Mode {
	case "expire":
		result := scope(s.db.WithContext(ctx).Model(&model.MemoryItem{})).Update("status", model.ItemStatusExpired)
		if result.Error != nil {
			return 0, wrapErr(CodeInternal, "expire failed", result.Error)
		}
		rowsAffected = result.RowsAffected

	case "hard":
		backup := req.BackupBeforeHardDelete == nil || *req.BackupBeforeHardDelete
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if backup {
				var items []model.MemoryItem
				if err := scope(tx.Model(&model.MemoryItem{})).Find(&items).Error; err != nil {
					return wrapErr(CodeInternal, "find items failed", err)
				}
				now := time.Now()
				purgeAfter := now.Add(DefaultDeletedRetention)
				for _, item := range items {
					data, _ := json.Marshal(item)
					deleted := model.DeletedItem{
						ID:               item.ID,
						OriginalDataJSON: string(data),
						DeletedAt:        now,
						PurgeAfter:       purgeAfter,
						Reason:           optionalString(req.Reason),
					}
					if err := tx.Create(&deleted).Error; err != nil {
						return wrapErr(CodeInternal, "backup before hard delete failed", err)
					}
				}
			}
			result := scope(tx.Model(&model.MemoryItem{})).Delete(&model.MemoryItem{})
			if result.Error != nil {
				return wrapErr(CodeInternal, "hard delete failed", result.Error)
			}
			rowsAffected = result.RowsAffected
			return nil
		})
		if err != nil {
			return 0, err
		}

	default: // "soft"
		result := scope(s.db.WithContext(ctx).Model(&model.MemoryItem{})).Update("status", model.ItemStatusDeleted)
		if result.Error != nil {
			return 0, wrapErr(CodeInternal, "soft delete failed", result.Error)
		}
		rowsAffected = result.RowsAffected
	}

	// Status changes bypass trg_fts_delete, so prune the index explicitly.
	if req.Mode != "hard" && len(affected) > 0 {
		if err := s.db.WithContext(ctx).Exec("DELETE FROM fts_memory WHERE item_id IN ?", affected).Error; err != nil {
			GetLogger().Warn("failed to prune fts index after forget", "error", err, "item_count", len(affected))
		}
	}

	// Callbacks
	for _, id := range affected {
		switch req.Mode {
		case "expire":
			if s.config.OnExpired != nil {
				s.config.OnExpired(ctx, id)
			}
		default:
			if s.config.OnDeleted != nil {
				s.config.OnDeleted(ctx, id)
			}
		}
	}

	eventType := model.EventTypeDelete
	if req.Mode == "expire" {
		eventType = model.EventTypeExpire
	}
	s.recordEvent(ctx, eventType, nil, strings.Join(targets, ","), map[string]interface{}{
		"mode":   req.Mode,
		"count":  int(rowsAffected),
		"reason": req.Reason,
	})

	if rowsAffected == 0 && len(req.ItemIDs) > 0 {
		return 0, wrapErr(CodeNotFound, "no memory matched the forget request", nil)
	}
	return int(rowsAffected), nil
}

// DefaultDeletedRetention is how long a hard-deleted row is kept in deleted_items before
// PurgeExpiredDeleted may remove it.
const DefaultDeletedRetention = 7 * 24 * time.Hour

func optionalString(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// UpdateRequest represents a request to update a memory.
type UpdateRequest struct {
	ItemID          string
	Namespace       string
	NamespaceType   model.NamespaceType
	Title           *string
	Content         *string
	Summary         *string
	Tags            []string
	Importance      *int
	Confidence      *float64
	ExpectedVersion int // For optimistic locking
}

// Update modifies a memory item with optimistic locking.
//
// Inside an isolation context the row must live in one of the caller's namespaces; the check
// is done in the same statement as the version compare, so an isolated caller cannot update
// (or probe) another tenant's row.
func (s *MemoryService) Update(ctx context.Context, req UpdateRequest) error {
	if req.ExpectedVersion <= 0 {
		return newErr(CodeValidation, "expected_version must be greater than 0")
	}
	if req.Importance != nil && (*req.Importance < 0 || *req.Importance > 100) {
		return newErr(CodeValidation, fmt.Sprintf("importance must be within 0-100, got %d", *req.Importance))
	}
	if req.Confidence != nil && (*req.Confidence < 0 || *req.Confidence > 1) {
		return newErr(CodeValidation, fmt.Sprintf("confidence must be within 0.0-1.0, got %v", *req.Confidence))
	}

	meta, isolated, err := resolveIsolation(ctx)
	if err != nil {
		return err
	}

	// Scope the update to what the caller may touch.
	scope := func(db *gorm.DB) *gorm.DB {
		query := db.WithContext(ctx).Model(&model.MemoryItem{}).Where("id = ?", req.ItemID)
		if isolated {
			return query.Where("namespace IN ?", buildAllowedNamespaces(meta, namespaceTypeFilter(req.NamespaceType)))
		}
		if ns := strings.TrimSpace(req.Namespace); ns != "" {
			return query.Where("namespace = ?", ns)
		}
		if req.NamespaceType != "" {
			return query.Where("namespace = ?", buildDefaultNamespace(req.NamespaceType))
		}
		return query
	}

	var item model.MemoryItem
	if err := scope(s.db).First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return wrapErr(CodeNotFound, "item not found", err)
		}
		return wrapErr(CodeInternal, "query failed", err)
	}

	// Build updates
	updates := map[string]interface{}{
		"version":    gorm.Expr("version + 1"),
		"updated_at": time.Now(),
	}

	if req.Title != nil {
		updates["title"] = *req.Title
	}
	if req.Content != nil {
		if strings.TrimSpace(*req.Content) == "" {
			return newErr(CodeValidation, "content must not be empty")
		}
		if len([]rune(*req.Content)) > MaxContentRunes {
			return newErr(CodeValidation, fmt.Sprintf("content exceeds %d runes", MaxContentRunes))
		}
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

	// Fresh statement: the scoped read above must not leak its WHERE clauses into this write
	// (GORM would emit an ambiguous "version" reference from the join/condition reuse).
	rowsAffected, err := updateRows(ctx, scope(s.db).Where("version = ?", req.ExpectedVersion), updates)
	if err != nil {
		return wrapErr(CodeInternal, "update failed", err)
	}
	if rowsAffected == 0 {
		var current model.MemoryItem
		err := s.db.WithContext(ctx).Select("id, version").First(&current, "id = ?", req.ItemID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return wrapErr(CodeNotFound, "item not found", err)
		}
		if err != nil {
			return wrapErr(CodeInternal, "failed to verify update conflict", err)
		}
		return wrapErr(CodeConflict, fmt.Sprintf("version conflict: expected %d, got %d", req.ExpectedVersion, current.Version), nil)
	}

	s.recordEvent(ctx, model.EventTypeUpdate, &req.ItemID, item.Namespace, nil)

	// Reload item and trigger callback
	if s.config.OnUpdated != nil {
		s.db.WithContext(ctx).First(&item, "id = ?", req.ItemID)
		s.config.OnUpdated(ctx, item)
	}

	return nil
}

// namespaceTypeFilter turns an optional namespace type into the buildAllowedNamespaces argument
// (empty means "all types").
func namespaceTypeFilter(nsType model.NamespaceType) []model.NamespaceType {
	if nsType == "" {
		return nil
	}
	return []model.NamespaceType{nsType}
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
// Expired rows are removed from the FTS index so they stop being searchable.
func (s *MemoryService) CleanupExpired(ctx context.Context) (int64, error) {
	now := time.Now()

	var itemIDs []string
	if err := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("status = ? AND expires_at IS NOT NULL AND expires_at < ?", model.ItemStatusActive, now).
		Pluck("id", &itemIDs).Error; err != nil {
		return 0, wrapErr(CodeInternal, "failed to resolve expired items", err)
	}
	if len(itemIDs) == 0 {
		return 0, nil
	}

	result := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("id IN ? AND status = ?", itemIDs, model.ItemStatusActive).
		Update("status", model.ItemStatusExpired)
	if result.Error != nil {
		return 0, wrapErr(CodeInternal, "cleanup expired failed", result.Error)
	}

	if err := s.db.WithContext(ctx).Exec("DELETE FROM fts_memory WHERE item_id IN ?", itemIDs).Error; err != nil {
		GetLogger().Warn("failed to prune fts index after cleanup", "error", err, "item_count", len(itemIDs))
	}

	// Trigger callbacks
	for _, id := range itemIDs {
		if s.config.OnExpired != nil {
			s.config.OnExpired(ctx, id)
		}
	}

	return result.RowsAffected, nil
}

// PurgeDeleted physically removes soft-deleted rows whose updated_at is older than the
// cutoff. A copy is archived in deleted_items (retention: DefaultDeletedRetention) so the
// purge stays reversible; call PurgeExpiredDeleted to actually drop that copy afterwards.
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

		// Archive in deleted_items
		now := time.Now()
		purgeAfter := now.Add(DefaultDeletedRetention)
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

		// Delete from memory_items (trg_fts_delete keeps the index in sync)
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

// PurgeExpiredDeleted irreversibly drops archived rows from deleted_items once their
// retention window has passed. This is the step that finally removes the retained copy of a
// "hard" delete, so a GDPR-style erasure is: Forget(mode=hard) -> PurgeExpiredDeleted.
func (s *MemoryService) PurgeExpiredDeleted(ctx context.Context, now time.Time) (int64, error) {
	result := s.db.WithContext(ctx).
		Where("purge_after <= ?", now).
		Delete(&model.DeletedItem{})
	if result.Error != nil {
		return 0, wrapErr(CodeInternal, "purge expired deleted_items failed", result.Error)
	}
	return result.RowsAffected, nil
}

// PurgeDeletedNow drops the archived copy of specific hard-deleted items immediately,
// ignoring the retention window. Use it when a subject demands immediate erasure.
func (s *MemoryService) PurgeDeletedNow(ctx context.Context, itemIDs []string) (int64, error) {
	if len(itemIDs) == 0 {
		return 0, newErr(CodeValidation, "item_ids must not be empty")
	}
	result := s.db.WithContext(ctx).Where("id IN ?", itemIDs).Delete(&model.DeletedItem{})
	if result.Error != nil {
		return 0, wrapErr(CodeInternal, "purge deleted_items failed", result.Error)
	}
	return result.RowsAffected, nil
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

	failed := make([]string, 0)
	for _, item := range items {
		if err := s.db.WithContext(ctx).Exec(`
			INSERT INTO fts_memory (tokenized_content, item_id)
			VALUES (?, ?)
		`, item.TokenizedText, item.ID).Error; err != nil {
			failed = append(failed, item.ID)
			continue
		}
	}

	if len(failed) > 0 {
		return wrapErr(
			CodeInternal,
			fmt.Sprintf("rebuild fts partially failed: %d items", len(failed)),
			errors.New(strings.Join(failed, ",")),
		)
	}

	ok, err := s.ValidateFTS(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return wrapErr(CodeInternal, "rebuild fts validation failed", nil)
	}

	return nil
}

// ValidateFTS validates that active memory rows and FTS rows are in sync.
func (s *MemoryService) ValidateFTS(ctx context.Context) (bool, error) {
	var activeCount int64
	if err := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("status = ?", model.ItemStatusActive).
		Count(&activeCount).Error; err != nil {
		return false, wrapErr(CodeInternal, "count active items failed", err)
	}

	var ftsCount int64
	row := s.db.WithContext(ctx).Raw("SELECT COUNT(*) FROM fts_memory").Row()
	if err := row.Scan(&ftsCount); err != nil {
		return false, wrapErr(CodeInternal, "count fts rows failed", err)
	}

	return activeCount == ftsCount, nil
}

type filterOptions struct {
	Namespaces     []string
	NamespaceTypes []model.NamespaceType
	TagsAny        []string
	TagsAll        []string
	TimeRangeStart *time.Time
	TimeRangeEnd   *time.Time
	IncludeExpired bool
	ExcludeItemIDs []string
	InstanceIDs    []string
	MinConfidence  float64
	MinImportance  int
}

func (s *MemoryService) baseFilteredQuery(ctx context.Context, opts filterOptions) *gorm.DB {
	query := s.db.WithContext(ctx).Model(&model.MemoryItem{})
	query = query.Where("status = ?", model.ItemStatusActive)

	if !opts.IncludeExpired {
		query = query.Where("expires_at IS NULL OR expires_at > ?", time.Now())
	}
	if len(opts.Namespaces) > 0 {
		query = query.Where("namespace IN ?", opts.Namespaces)
	}
	if len(opts.NamespaceTypes) > 0 {
		query = query.Where("namespace_type IN ?", opts.NamespaceTypes)
	}
	if len(opts.InstanceIDs) > 0 {
		query = query.Where("id IN ?", opts.InstanceIDs)
	}
	if len(opts.TagsAll) > 0 {
		for _, tag := range opts.TagsAll {
			query = query.Where("tags_json LIKE ? ESCAPE '\\'", fmt.Sprintf("%%\"%s\"%%", escapeLikePattern(tag)))
		}
	}
	if len(opts.TagsAny) > 0 {
		conditions := ""
		params := make([]interface{}, 0, len(opts.TagsAny))
		for i, tag := range opts.TagsAny {
			if i > 0 {
				conditions += " OR "
			}
			conditions += "tags_json LIKE ? ESCAPE '\\'"
			params = append(params, fmt.Sprintf("%%\"%s\"%%", escapeLikePattern(tag)))
		}
		query = query.Where("("+conditions+")", params...)
	}
	if opts.TimeRangeStart != nil {
		query = query.Where("created_at >= ?", *opts.TimeRangeStart)
	}
	if opts.TimeRangeEnd != nil {
		query = query.Where("created_at <= ?", *opts.TimeRangeEnd)
	}
	if len(opts.ExcludeItemIDs) > 0 {
		query = query.Where("id NOT IN ?", opts.ExcludeItemIDs)
	}
	if opts.MinConfidence > 0 {
		query = query.Where("confidence >= ?", opts.MinConfidence)
	}
	if opts.MinImportance > 0 {
		query = query.Where("importance >= ?", opts.MinImportance)
	}
	return query
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}
