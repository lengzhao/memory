package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
)

// DefaultMinConfidence is the confidence floor applied when a request leaves MinConfidence unset.
// Rows written with Confidence 0 (a host that does not score its memories) stay recallable.
const DefaultMinConfidence = 0.5

// RecallRequest describes a recall.
//
// Scope decides which namespaces are readable; the request can only narrow that set by type.
type RecallRequest struct {
	Query          string
	NamespaceTypes []model.NamespaceType
	TagsAny        []string
	TagsAll        []string
	TimeRangeStart *time.Time
	TimeRangeEnd   *time.Time
	IncludeExpired bool
	// InstanceIDs restricts the recall to specific memories.
	InstanceIDs []string
	// MinConfidence filters on stored confidence; SkipConfidenceFilter disables the filter.
	MinConfidence        float64
	SkipConfidenceFilter bool
	MinImportance        int
	TopK                 int
	ExcludeItemIDs       []string
}

// MemoryHit is a recalled memory with its relevance breakdown.
type MemoryHit struct {
	model.MemoryItem
	Score           float64
	FTSScore        float64
	RecencyScore    float64
	ImportanceScore float64
	ConfidenceScore float64
	MatchReasons    []string
}

// Recall returns the most relevant memories for the query, ranked by the namespace policy
// weights. The text component of the score is the BM25 relevance of each match, not a constant.
func (s *MemoryService) Recall(ctx context.Context, scope Scope, req RecallRequest) ([]MemoryHit, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	for _, t := range req.NamespaceTypes {
		if !validNamespaceType(t) {
			return nil, newErr(CodeValidation, fmt.Sprintf("invalid namespace_type %q", t))
		}
	}
	if req.TopK <= 0 {
		req.TopK = 10
	}

	namespaces := scope.Namespaces(req.NamespaceTypes...)
	query := baseFilteredQuery(ctx, s.db, filterOptions{
		Namespaces:     namespaces,
		TagsAny:        req.TagsAny,
		TagsAll:        req.TagsAll,
		TimeRangeStart: req.TimeRangeStart,
		TimeRangeEnd:   req.TimeRangeEnd,
		IncludeExpired: req.IncludeExpired,
		ExcludeItemIDs: req.ExcludeItemIDs,
		InstanceIDs:    req.InstanceIDs,
		MinConfidence:  resolveMinConfidence(req.MinConfidence, req.SkipConfidenceFilter),
		MinImportance:  req.MinImportance,
	})

	var (
		matchedIDs    []string
		ftsScores     map[string]float64
		scoreSource   textScoreSource
		substringScan bool
	)
	if req.Query != "" {
		candidateLimit := req.TopK * 3
		if candidateLimit < 50 {
			candidateLimit = 50
		}
		var err error
		matchedIDs, ftsScores, scoreSource, err = s.search(ctx, req.Query, candidateLimit)
		if err != nil {
			return nil, err
		}
		if scoreSource == textScoreSubstring {
			// The tokenized index could not serve this query at all. Scan for the literal text with
			// escaped wildcards, so the query cannot smuggle LIKE metacharacters.
			substringScan = true
			pattern := "%" + escapeLikePattern(req.Query) + "%"
			query = query.Where(
				"(title LIKE ? ESCAPE '\\' OR content LIKE ? ESCAPE '\\' OR summary LIKE ? ESCAPE '\\')",
				pattern, pattern, pattern,
			)
		} else {
			query = query.Where("id IN ?", matchedIDs)
		}
	}

	var items []model.MemoryItem
	if err := query.Limit(req.TopK * 2).Find(&items).Error; err != nil {
		return nil, wrapErr(CodeInternal, "recall query failed", err)
	}

	hits, err := s.scoreAndRank(ctx, items, req.Query, scoreSource, ftsScores)
	if err != nil {
		return nil, err
	}
	if len(hits) > req.TopK {
		hits = hits[:req.TopK]
	}

	if len(matchedIDs) == 0 || substringScan {
		matchedIDs = make([]string, 0, len(hits))
		for _, hit := range hits {
			matchedIDs = append(matchedIDs, hit.ID)
		}
	}
	s.enqueueAccessStats(matchedIDs)
	return hits, nil
}

// ListRequest describes a chronological listing.
type ListRequest struct {
	NamespaceTypes       []model.NamespaceType
	TagsAny              []string
	TagsAll              []string
	TimeRangeStart       *time.Time
	TimeRangeEnd         *time.Time
	IncludeExpired       bool
	InstanceIDs          []string
	MinConfidence        float64
	SkipConfidenceFilter bool
	MinImportance        int
	TopK                 int
	Offset               int
	ExcludeItemIDs       []string
	Order                string // "desc" (default) or "asc", by created_at
}

// List returns memories by creation time, newest first unless Order is "asc".
func (s *MemoryService) List(ctx context.Context, scope Scope, req ListRequest) ([]model.MemoryItem, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	for _, t := range req.NamespaceTypes {
		if !validNamespaceType(t) {
			return nil, newErr(CodeValidation, fmt.Sprintf("invalid namespace_type %q", t))
		}
	}
	if req.TopK <= 0 {
		req.TopK = 10
	}

	query := baseFilteredQuery(ctx, s.db, filterOptions{
		Namespaces:     scope.Namespaces(req.NamespaceTypes...),
		TagsAny:        req.TagsAny,
		TagsAll:        req.TagsAll,
		TimeRangeStart: req.TimeRangeStart,
		TimeRangeEnd:   req.TimeRangeEnd,
		IncludeExpired: req.IncludeExpired,
		ExcludeItemIDs: req.ExcludeItemIDs,
		InstanceIDs:    req.InstanceIDs,
		MinConfidence:  resolveMinConfidence(req.MinConfidence, req.SkipConfidenceFilter),
		MinImportance:  req.MinImportance,
	})

	order := "DESC"
	if strings.EqualFold(req.Order, "asc") {
		order = "ASC"
	}

	var items []model.MemoryItem
	if err := query.
		Order("created_at " + order).
		Order("id " + order).
		Offset(req.Offset).
		Limit(req.TopK).
		Find(&items).Error; err != nil {
		return nil, wrapErr(CodeInternal, "list query failed", err)
	}
	return items, nil
}

// scoreAndRank applies the policy weights over the candidate rows.
func (s *MemoryService) scoreAndRank(ctx context.Context, items []model.MemoryItem, query string, source textScoreSource, ftsScores map[string]float64) ([]MemoryHit, error) {
	now := time.Now()
	hits := make([]MemoryHit, 0, len(items))
	weightCache := make(map[string]rankWeights)

	for _, item := range items {
		hit := MemoryHit{MemoryItem: item}

		// Text relevance. BM25 and a substring hit live on different scales, so the substring case
		// gets a fixed, documented value rather than being averaged with BM25 numbers. The reason is
		// readable by callers through MatchReasons.
		if query != "" {
			switch source {
			case textScoreFTS:
				hit.FTSScore = ftsScores[item.ID]
				hit.MatchReasons = append(hit.MatchReasons,
					"fts_match", "coverage:"+itoa(int(hit.FTSScore*100))+"%")
			default:
				hit.FTSScore = substringMatchScore
				hit.MatchReasons = append(hit.MatchReasons, "substring_match")
			}
		}

		hit.RecencyScore = expDecay(now.Sub(item.CreatedAt).Hours(), recencyHalfLifeHours)
		hit.ImportanceScore = float64(item.Importance) / 100.0
		hit.ConfidenceScore = item.Confidence
		accessBoost := min(0.1, float64(item.AccessCount)/100.0)

		weights, err := rankWeightsForNamespace(ctx, s.policyManager, item.Namespace, weightCache)
		if err != nil {
			return nil, err
		}
		hit.Score = normalizeScore(weights.FTS*hit.FTSScore +
			weights.Recency*hit.RecencyScore +
			weights.Importance*hit.ImportanceScore +
			weights.Confidence*hit.ConfidenceScore + accessBoost)

		hits = append(hits, hit)
	}

	// Stable ordering: score, then recency, then id, so equal scores do not shuffle between calls.
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

// recencyHalfLifeHours is the half-life of the recency term: a week-old memory keeps half the
// weight of a fresh one.
const recencyHalfLifeHours = 168

// substringMatchScore is the text component for a literal substring hit. A substring scan cannot
// rank, so this is a fixed mid-scale value: it keeps such memories in the result set without
// pretending to know how relevant they are.
const substringMatchScore = 0.5

type rankWeights struct {
	FTS        float64
	Recency    float64
	Importance float64
	Confidence float64
}

func rankWeightsForNamespace(ctx context.Context, pm *PolicyManager, namespace string, cache map[string]rankWeights) (rankWeights, error) {
	if w, ok := cache[namespace]; ok {
		return w, nil
	}
	policy, err := pm.GetPolicy(ctx, namespace)
	if err != nil {
		return rankWeights{}, err
	}
	fts, recency, importance, confidence := pm.GetRankWeights(policy)
	w := rankWeights{FTS: fts, Recency: recency, Importance: importance, Confidence: confidence}

	sum := w.FTS + w.Recency + w.Importance + w.Confidence
	switch {
	case sum <= 0:
		w = rankWeights{FTS: 0.55, Recency: 0.20, Importance: 0.15, Confidence: 0.10}
	case math.Abs(sum-1.0) > 0.0001:
		w.FTS, w.Recency, w.Importance, w.Confidence = w.FTS/sum, w.Recency/sum, w.Importance/sum, w.Confidence/sum
	}
	cache[namespace] = w
	return w, nil
}

// expDecay is an exponential decay with a half-life: 0.5 ** (age / halfLife).
func expDecay(hoursAgo, halfLifeHours float64) float64 {
	if halfLifeHours <= 0 {
		return 0
	}
	if hoursAgo < 0 {
		return 1.0
	}
	return math.Pow(0.5, hoursAgo/halfLifeHours)
}

// normalizeScore clamps a score into [0,1] and drops NaN.
func normalizeScore(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func resolveMinConfidence(requested float64, skip bool) float64 {
	if skip || requested < 0 {
		return 0
	}
	if requested == 0 {
		return DefaultMinConfidence
	}
	return requested
}

// textScoreSource records how the text component of a score was obtained. Ranking depends on it:
// a BM25 relevance and a substring hit are not comparable, so the caller must know which one it
// received. The scale steps down; nothing is averaged across them.
type textScoreSource int

const (
	// textScoreNone means the request had no query text.
	textScoreNone textScoreSource = iota
	// textScoreFTS is a BM25 relevance from the search index.
	textScoreFTS
	// textScoreSubstring is a literal substring hit over the raw columns.
	textScoreSubstring
)

func (s textScoreSource) String() string {
	switch s {
	case textScoreFTS:
		return "fts"
	case textScoreSubstring:
		return "substring"
	default:
		return "none"
	}
}

// search runs the text search and reports how the result was produced.
//
// It prefers the tokenized index and degrades to a substring scan only when the index cannot serve
// the query at all. The degradation is a policy decision (a substring scan cannot rank), not a
// silent fallback that mixes incomparable scores.
func (s *MemoryService) search(ctx context.Context, query string, limit int) ([]string, map[string]float64, textScoreSource, error) {
	tokens := strings.Fields(TokenizeQuery(query))
	if len(tokens) == 0 {
		// A query made only of punctuation has no index terms: the substring scan is the only way
		// to answer it literally.
		return nil, nil, textScoreSubstring, nil
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
	`, ftsQuery(tokens), limit).Scan(&results).Error
	if err != nil {
		// A malformed query must not fail the recall.
		GetLogger().Warn("fts search failed, falling back to substring scan", "error", err)
		return nil, nil, textScoreSubstring, nil
	}
	if len(results) == 0 {
		return nil, nil, textScoreSubstring, nil
	}

	ids := make([]string, 0, len(results))
	scores := make(map[string]float64, len(results))
	for _, r := range results {
		ids = append(ids, r.ItemID)
		scores[r.ItemID] = tokenCoverage(tokens, r.TokenizedText)
	}
	return ids, scores, textScoreFTS, nil
}

// tokenCoverage is the fraction of the query's tokens that the memory contains, i.e. how much of
// what was asked for this memory actually covers.
//
// It is deliberately not BM25-derived. FTS5's bm25() is IDF-weighted, so in a small database (or a
// single-document match) the IDF term degenerates and even a perfect match scores ~0 — exactly the
// case an embedded per-user memory database is in. Coverage is IDF-free: it means the same thing at
// any corpus size, and its ordering matches intuition for natural-language queries.
func tokenCoverage(queryTokens []string, indexedText string) float64 {
	if len(queryTokens) == 0 {
		return 0
	}
	indexed := make(map[string]struct{}, 16)
	for _, token := range strings.Fields(indexedText) {
		indexed[token] = struct{}{}
	}
	matched := 0
	for _, token := range queryTokens {
		if _, ok := indexed[token]; ok {
			matched++
		}
	}
	return float64(matched) / float64(len(queryTokens))
}

// ftsQuery builds an OR query over quoted tokens. Quoting makes the tokens literals, so user input
// cannot inject FTS operators.
func ftsQuery(tokens []string) string {
	quoted := make([]string, 0, len(tokens))
	for _, token := range tokens {
		token = strings.ReplaceAll(token, `"`, "")
		if token == "" {
			continue
		}
		quoted = append(quoted, `"`+token+`"`)
	}
	return strings.Join(quoted, " OR ")
}

// escapeLikePattern escapes LIKE metacharacters so user input matches literally.
func escapeLikePattern(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

type filterOptions struct {
	Namespaces     []string
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

// baseFilteredQuery builds the read predicate shared by Recall and List. Every read is expressed
// against an explicit namespace set, so a read cannot accidentally span tenants.
func baseFilteredQuery(ctx context.Context, db *gorm.DB, opts filterOptions) *gorm.DB {
	query := db.WithContext(ctx).Model(&model.MemoryItem{})
	query = query.Where("status = ?", model.ItemStatusActive)

	if !opts.IncludeExpired {
		query = query.Where("expires_at IS NULL OR expires_at > ?", time.Now())
	}
	if len(opts.Namespaces) > 0 {
		query = query.Where("namespace IN ?", opts.Namespaces)
	}
	if len(opts.InstanceIDs) > 0 {
		query = query.Where("id IN ?", opts.InstanceIDs)
	}
	for _, tag := range opts.TagsAll {
		query = query.Where("tags_json LIKE ? ESCAPE '\\'", `%"`+escapeLikePattern(tag)+`"%`)
	}
	if len(opts.TagsAny) > 0 {
		conditions := make([]string, 0, len(opts.TagsAny))
		params := make([]interface{}, 0, len(opts.TagsAny))
		for _, tag := range opts.TagsAny {
			conditions = append(conditions, "tags_json LIKE ? ESCAPE '\\'")
			params = append(params, `%"`+escapeLikePattern(tag)+`"%`)
		}
		query = query.Where("("+strings.Join(conditions, " OR ")+")", params...)
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

// updateAccessStats records that memories were surfaced. It runs off the request path: a recall
// must not fail because usage bookkeeping did.
func (s *MemoryService) updateAccessStats(ctx context.Context, itemIDs []string) {
	if len(itemIDs) == 0 {
		return
	}
	result := s.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("id IN ?", itemIDs).
		Updates(map[string]interface{}{
			"access_count":   gorm.Expr("access_count + 1"),
			"last_access_at": time.Now(),
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
