// Package service provides the memory decision engine: it lets an LLM pick between ADD, UPDATE,
// DELETE, IGNORE and MERGE for freshly extracted candidates, then applies the decision through
// MemoryService so that indexing, versioning and auditing stay consistent.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
)

// DecisionType is the action chosen for one candidate.
type DecisionType string

const (
	DecisionAdd    DecisionType = "ADD"
	DecisionUpdate DecisionType = "UPDATE"
	DecisionDelete DecisionType = "DELETE"
	DecisionIgnore DecisionType = "IGNORE"
	DecisionMerge  DecisionType = "MERGE"
)

// MemoryDecision is the decision for a single candidate.
type MemoryDecision struct {
	Decision      DecisionType `json:"decision"`
	TargetID      string       `json:"target_id,omitempty"`
	Reason        string       `json:"reason"`
	Confidence    float64      `json:"confidence"`
	MergedContent string       `json:"merged_content,omitempty"`
	MergedTitle   string       `json:"merged_title,omitempty"`
	NewImportance int          `json:"new_importance,omitempty"`
}

// DecisionRequest bundles the candidates with the existing memories they resemble.
type DecisionRequest struct {
	Candidates      []ExtractedMemory `json:"candidates"`
	SimilarMemories []SimilarMemory   `json:"similar_memories"`
	DialogContext   string            `json:"dialog_context"`
}

// SimilarMemory is an existing memory that may cover the same topic as a candidate.
type SimilarMemory struct {
	ID         string   `json:"id"`
	Namespace  string   `json:"namespace"`
	Title      string   `json:"title"`
	Content    string   `json:"content"`
	Summary    string   `json:"summary"`
	Tags       []string `json:"tags"`
	Importance int      `json:"importance"`
	Confidence float64  `json:"confidence"`
	CreatedAt  string   `json:"created_at"`
	Similarity float64  `json:"similarity"`
}

// DecisionResult is the model's answer plus its cost.
type DecisionResult struct {
	Decisions      []MemoryDecision `json:"decisions"`
	TotalTokens    int              `json:"total_tokens"`
	ProcessingTime int              `json:"processing_time_ms"`
}

// ExecutionResult reports what was actually written.
type ExecutionResult struct {
	Added   []string
	Updated []string
	Deleted []string
	Ignored []string
	Merged  []string
	Errors  []string
}

// DecisionEngine turns extraction candidates into memory writes.
//
// It is scope-aware: every lookup and every write is constrained to the scope it was called with,
// and an ID proposed by the model is re-checked against the candidate's namespace type, so a
// hallucinated or hostile target cannot reach outside the namespace it was classified into.
type DecisionEngine struct {
	db *gorm.DB
}

// NewDecisionEngine creates a decision engine.
func NewDecisionEngine(db *gorm.DB) *DecisionEngine {
	return &DecisionEngine{db: db}
}

// FindSimilarMemories returns the existing memories most similar to a candidate, ordered by the
// policy-weighted combination of BM25 relevance and tag overlap.
func (de *DecisionEngine) FindSimilarMemories(ctx context.Context, scope Scope, candidate ExtractedMemory, topK int) ([]SimilarMemory, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	nsType, err := normalizeNamespaceType(candidate.Namespace)
	if err != nil {
		return nil, err
	}
	if topK <= 0 {
		topK = 5
	}

	query := buildFTSQuery(candidate.Title, candidate.Content, candidate.Tags)
	if query == "" {
		return nil, nil
	}

	tokens := strings.Fields(TokenizeQuery(query))
	if len(tokens) == 0 {
		return nil, nil
	}

	// Same relevance notion as Recall (token coverage), so "similarity 0.8" means the same thing in
	// both places and the decision prompt's thresholds stay meaningful. Candidates are pre-filtered
	// by the index so the LIKE scan never runs here.
	itemIDs, coverage, source, err := NewMemoryService(de.db).search(ctx, query, topK*2)
	if err != nil {
		return nil, err
	}
	if source != textScoreFTS || len(itemIDs) == 0 {
		return nil, nil
	}

	var items []model.MemoryItem
	if err := de.db.WithContext(ctx).
		Where("id IN ? AND status = ?", itemIDs, model.ItemStatusActive).
		Where("namespace = ?", scope.Namespace(nsType)).
		Find(&items).Error; err != nil {
		return nil, wrapErr(CodeInternal, "failed to load similar memories", err)
	}

	weights, err := de.similarityWeights(ctx, scope, nsType)
	if err != nil {
		return nil, err
	}

	similar := make([]SimilarMemory, 0, len(items))
	for _, item := range items {
		ftsSim := coverage[item.ID]
		tagSim := calculateTagOverlap(candidate.Tags, parseTags(item.TagsJSON))
		finalSim := weights.FTSSimilarity*ftsSim + weights.TagOverlap*tagSim
		if finalSim <= similarityThreshold {
			continue
		}
		similar = append(similar, SimilarMemory{
			ID:         item.ID,
			Namespace:  item.Namespace,
			Title:      item.Title,
			Content:    item.Content,
			Summary:    item.Summary,
			Tags:       parseTags(item.TagsJSON),
			Importance: item.Importance,
			Confidence: item.Confidence,
			CreatedAt:  item.CreatedAt.Format(time.RFC3339),
			Similarity: finalSim,
		})
	}

	sort.Slice(similar, func(i, j int) bool {
		if similar[i].Similarity != similar[j].Similarity {
			return similar[i].Similarity > similar[j].Similarity
		}
		return similar[i].ID < similar[j].ID
	})
	if len(similar) > topK {
		similar = similar[:topK]
	}
	return similar, nil
}

// similarityThreshold keeps rows with almost no topical overlap out of the decision prompt. It
// applies to the coverage-based similarity (see tokenCoverage), so it is corpus-size independent.
const similarityThreshold = 0.2

type decisionSimilarityWeights struct {
	FTSSimilarity float64
	TagOverlap    float64
}

func (de *DecisionEngine) similarityWeights(ctx context.Context, scope Scope, nsType model.NamespaceType) (decisionSimilarityWeights, error) {
	policy, err := NewPolicyManager(de.db).GetPolicy(ctx, scope.Namespace(nsType))
	if err != nil {
		return decisionSimilarityWeights{}, err
	}
	return parseDecisionSimilarityWeights(policy.RankWeightsJSON), nil
}

func parseDecisionSimilarityWeights(rankWeightsJSON string) decisionSimilarityWeights {
	weights := decisionSimilarityWeights{FTSSimilarity: 0.7, TagOverlap: 0.3}
	if strings.TrimSpace(rankWeightsJSON) == "" {
		return weights
	}
	var raw map[string]float64
	if err := json.Unmarshal([]byte(rankWeightsJSON), &raw); err != nil {
		return weights
	}
	if v, ok := raw["fts_similarity"]; ok {
		weights.FTSSimilarity = v
	}
	if v, ok := raw["tag_overlap"]; ok {
		weights.TagOverlap = v
	}
	sum := weights.FTSSimilarity + weights.TagOverlap
	if sum <= 0 {
		return decisionSimilarityWeights{FTSSimilarity: 0.7, TagOverlap: 0.3}
	}
	weights.FTSSimilarity /= sum
	weights.TagOverlap /= sum
	return weights
}

// buildFTSQuery assembles the search text for a candidate: title, the head of the content, and tags.
func buildFTSQuery(title, content string, tags []string) string {
	parts := make([]string, 0, 3)
	if clean := sanitizeFTSQuery(title); clean != "" {
		parts = append(parts, clean)
	}
	if content != "" {
		runes := []rune(content)
		if len(runes) > 200 {
			content = string(runes[:200])
		}
		if clean := sanitizeFTSQuery(content); clean != "" {
			parts = append(parts, clean)
		}
	}
	if len(tags) > 0 {
		tagParts := make([]string, 0, len(tags))
		for _, tag := range tags {
			if clean := sanitizeFTSQuery(tag); clean != "" {
				tagParts = append(tagParts, clean)
			}
		}
		if len(tagParts) > 0 {
			parts = append(parts, strings.Join(tagParts, " "))
		}
	}
	return strings.Join(parts, " ")
}

// sanitizeFTSQuery strips characters that would change the meaning of an FTS query.
func sanitizeFTSQuery(text string) string {
	cleaned := strings.NewReplacer(
		`"`, " ", "*", " ", "(", " ", ")", " ", "-", " ", "~", " ",
		"\n", " ", "\t", " ",
	).Replace(text)
	return strings.Join(strings.Fields(cleaned), " ")
}

// Decide asks the model which action to take for each candidate.
func (de *DecisionEngine) Decide(ctx context.Context, llmConfig model.LLMConfig, req DecisionRequest) (*DecisionResult, error) {
	start := time.Now()
	decisions, tokens, err := de.callDecisionLLM(ctx, llmConfig, de.buildDecisionPrompt(req))
	if err != nil {
		return nil, err
	}
	return &DecisionResult{
		Decisions:      decisions,
		TotalTokens:    tokens,
		ProcessingTime: int(time.Since(start).Milliseconds()),
	}, nil
}

func (de *DecisionEngine) buildDecisionPrompt(req DecisionRequest) string {
	var prompt strings.Builder
	prompt.WriteString(`You are a memory management AI. Choose the best action for each new candidate.

ACTIONS:
1. ADD: no existing memory covers this topic
2. UPDATE: an existing memory is out of date and the candidate replaces it
3. DELETE: the candidate contradicts an existing memory that should not survive
4. IGNORE: the candidate is a duplicate or adds nothing
5. MERGE: the candidate and an existing memory share a topic but each holds unique facts

CRITERIA:
- Similarity > 0.8: prefer UPDATE, MERGE or DELETE over ADD
- Same topic, different facts: MERGE and preserve every unique fact
- Contradictory facts: DELETE the superseded memory (or UPDATE it)
- Pure duplicate: IGNORE

`)

	prompt.WriteString("NEW CANDIDATES:\n")
	for i, c := range req.Candidates {
		fmt.Fprintf(&prompt, "\n[CANDIDATE %d]\nTempID: %s\nNamespace: %s\nTitle: %s\nContent: %s\nTags: %v\nImportance: %d, Confidence: %.2f\n",
			i, c.TempID, c.Namespace, c.Title, c.Content, c.Tags, c.Importance, c.Confidence)
	}

	prompt.WriteString("\n\nEXISTING SIMILAR MEMORIES:\n")
	for i, s := range req.SimilarMemories {
		fmt.Fprintf(&prompt, "\n[MEMORY %d]\nID: %s\nNamespace: %s\nTitle: %s\nContent: %s\nTags: %v\nImportance: %d, Similarity: %.2f\n",
			i, s.ID, s.Namespace, s.Title, s.Content, s.Tags, s.Importance, s.Similarity)
	}

	prompt.WriteString(`

Return JSON:
{
  "decisions": [
    {
      "decision": "ADD|UPDATE|DELETE|IGNORE|MERGE",
      "target_id": "existing_id_for_update_delete_merge",
      "reason": "explanation",
      "confidence": 0.9,
      "merged_content": "for MERGE: combined content",
      "merged_title": "for MERGE: combined title",
      "new_importance": 75
    }
  ]
}

Exactly one decision per candidate, in the order the candidates were given.`)
	return prompt.String()
}

func (de *DecisionEngine) callDecisionLLM(ctx context.Context, cfg model.LLMConfig, prompt string) ([]MemoryDecision, int, error) {
	messages := []openAIMessage{
		{Role: "system", Content: "You are a precise memory management AI. Decide based on semantic similarity and factual overlap."},
		{Role: "user", Content: prompt},
	}
	apiResp, err := callLLM(ctx, cfg, messages, decisionTemperature)
	if err != nil {
		return nil, 0, err
	}

	var result struct {
		Decisions []MemoryDecision `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(apiResp.Choices[0].Message.Content), &result); err != nil {
		return nil, apiResp.Usage.TotalTokens, fmt.Errorf("failed to parse decisions: %w", err)
	}
	return result.Decisions, apiResp.Usage.TotalTokens, nil
}

// decisionTemperature keeps the model close to the prompt; deciding is classification, not writing.
const decisionTemperature = 0.2

// ExecuteDecisions applies the decisions through MemoryService.
//
// Unrecognised or unusable decisions are reported as errors and skipped: defaulting them to ADD
// would quietly turn a parsing problem into duplicate memories.
func (de *DecisionEngine) ExecuteDecisions(ctx context.Context, scope Scope, candidates []ExtractedMemory, decisions []MemoryDecision) (*ExecutionResult, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if len(decisions) != len(candidates) {
		return nil, fmt.Errorf("decision count (%d) doesn't match candidate count (%d)", len(decisions), len(candidates))
	}

	svc := NewMemoryService(de.db)
	result := &ExecutionResult{Added: []string{}, Updated: []string{}, Deleted: []string{}, Ignored: []string{}, Merged: []string{}}

	for i, decision := range decisions {
		candidate := candidates[i]
		var (
			id  string
			err error
		)
		switch decision.Decision {
		case DecisionAdd:
			id, err = de.applyAdd(ctx, svc, scope, candidate)
			if err == nil {
				result.Added = append(result.Added, id)
			}
		case DecisionUpdate:
			id, err = de.applyUpdate(ctx, svc, scope, decision, candidate)
			if err == nil {
				result.Updated = append(result.Updated, id)
			}
		case DecisionDelete:
			id, err = de.applyDelete(ctx, svc, scope, decision, candidate)
			if err == nil {
				result.Deleted = append(result.Deleted, id)
			}
		case DecisionMerge:
			id, err = de.applyMerge(ctx, svc, scope, decision, candidate)
			if err == nil {
				result.Merged = append(result.Merged, id)
			}
		case DecisionIgnore:
			result.Ignored = append(result.Ignored, candidate.TempID)
			continue
		default:
			err = fmt.Errorf("unknown decision %q", decision.Decision)
		}
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s failed for %s: %v", decision.Decision, candidate.TempID, err))
		}
	}
	return result, nil
}

func (de *DecisionEngine) applyAdd(ctx context.Context, svc *MemoryService, scope Scope, candidate ExtractedMemory) (string, error) {
	return svc.Remember(ctx, scope, RememberRequest{
		NamespaceType: candidate.Namespace,
		Title:         candidate.Title,
		Content:       candidate.Content,
		Summary:       candidate.Summary,
		Tags:          candidate.Tags,
		SourceType:    model.SourceTypeAgent,
		Importance:    candidate.Importance,
		Confidence:    candidate.Confidence,
	})
}

func (de *DecisionEngine) applyUpdate(ctx context.Context, svc *MemoryService, scope Scope, decision MemoryDecision, candidate ExtractedMemory) (string, error) {
	if decision.TargetID == "" {
		return "", fmt.Errorf("target_id is required")
	}
	target, err := de.loadTarget(ctx, scope, decision.TargetID, candidate)
	if err != nil {
		return "", err
	}

	req := UpdateRequest{ItemID: target.ID, ExpectedVersion: target.Version}
	if title := strings.TrimSpace(candidate.Title); title != "" {
		req.Title = &title
	}
	if content := strings.TrimSpace(candidate.Content); content != "" {
		req.Content = &content
	}
	if summary := strings.TrimSpace(candidate.Summary); summary != "" {
		req.Summary = &summary
	}
	if len(candidate.Tags) > 0 {
		req.Tags = candidate.Tags
	}
	if decision.NewImportance > 0 {
		req.Importance = &decision.NewImportance
	}
	if err := svc.Update(ctx, scope, req); err != nil {
		return "", err
	}
	return target.ID, nil
}

func (de *DecisionEngine) applyDelete(ctx context.Context, svc *MemoryService, scope Scope, decision MemoryDecision, candidate ExtractedMemory) (string, error) {
	if decision.TargetID == "" {
		return "", fmt.Errorf("target_id is required")
	}
	target, err := de.loadTarget(ctx, scope, decision.TargetID, candidate)
	if err != nil {
		return "", err
	}
	// Deletion proposed by a model is always reversible: ForgetSoft keeps the row (and its audit
	// trail) recoverable, whereas a hard delete would make a bad decision unrecoverable.
	if _, err := svc.Forget(ctx, scope, ForgetRequest{
		ItemIDs:       []string{target.ID},
		NamespaceType: target.NamespaceType,
		Mode:          ForgetSoft,
		Reason:        decision.Reason,
	}); err != nil {
		return "", err
	}
	return target.ID, nil
}

func (de *DecisionEngine) applyMerge(ctx context.Context, svc *MemoryService, scope Scope, decision MemoryDecision, candidate ExtractedMemory) (string, error) {
	if decision.TargetID == "" {
		return "", fmt.Errorf("target_id is required")
	}
	target, err := de.loadTarget(ctx, scope, decision.TargetID, candidate)
	if err != nil {
		return "", err
	}

	mergedTitle := strings.TrimSpace(decision.MergedTitle)
	if mergedTitle == "" {
		mergedTitle = target.Title
		if len(candidate.Title) > len(mergedTitle) {
			mergedTitle = candidate.Title
		}
	}
	mergedContent := strings.TrimSpace(decision.MergedContent)
	if mergedContent == "" {
		mergedContent = mergeContent(target.Content, candidate.Content)
	}
	mergedTags := mergeTags(parseTags(target.TagsJSON), candidate.Tags)

	newImportance := target.Importance
	if decision.NewImportance > 0 {
		newImportance = decision.NewImportance
	} else if candidate.Importance > target.Importance {
		newImportance = candidate.Importance
	}
	mergedConfidence := (target.Confidence + candidate.Confidence) / 2

	req := UpdateRequest{
		ItemID:          target.ID,
		Title:           &mergedTitle,
		Content:         &mergedContent,
		Tags:            mergedTags,
		Importance:      &newImportance,
		Confidence:      &mergedConfidence,
		ExpectedVersion: target.Version,
	}
	if summary := strings.TrimSpace(candidate.Summary); summary != "" {
		req.Summary = &summary
	}
	if err := svc.Update(ctx, scope, req); err != nil {
		return "", err
	}

	record := model.MemoryMerge{
		ID:            model.GenerateID(),
		TargetID:      target.ID,
		SourceContent: candidate.Content,
		SourceTitle:   candidate.Title,
		MergedContent: mergedContent,
		MergedTitle:   mergedTitle,
		Reason:        decision.Reason,
		MergeType:     "auto",
		CreatedAt:     time.Now(),
	}
	if err := de.db.WithContext(ctx).Create(&record).Error; err != nil {
		GetLogger().Warn("failed to record memory merge", "error", err, "target_id", target.ID)
	}
	return target.ID, nil
}

// loadTarget resolves a memory the model referred to, constrained to the scope and to the
// namespace type the candidate was classified into.
func (de *DecisionEngine) loadTarget(ctx context.Context, scope Scope, targetID string, candidate ExtractedMemory) (model.MemoryItem, error) {
	nsType, err := normalizeNamespaceType(candidate.Namespace)
	if err != nil {
		return model.MemoryItem{}, err
	}
	var target model.MemoryItem
	if err := de.db.WithContext(ctx).
		Where("id = ?", targetID).
		Where("namespace = ?", scope.Namespace(nsType)).
		First(&target).Error; err != nil {
		return model.MemoryItem{}, fmt.Errorf("target memory %s not found in this scope and namespace: %w", targetID, err)
	}
	return target, nil
}

// mergeContent combines two versions of a topic, preferring the richer text and otherwise
// appending the new facts under an explicit marker so nothing is silently lost.
func mergeContent(existing, incoming string) string {
	if existing == incoming {
		return existing
	}
	if strings.Contains(existing, incoming) {
		return existing
	}
	if strings.Contains(incoming, existing) {
		return incoming
	}
	if strings.TrimSpace(existing) == "" {
		return incoming
	}
	if strings.TrimSpace(incoming) == "" {
		return existing
	}
	return existing + "\n\n[Additional Info]\n" + incoming
}

func calculateTagOverlap(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[string]bool, len(a))
	for _, t := range a {
		set[strings.ToLower(t)] = true
	}
	overlap := 0
	for _, t := range b {
		if set[strings.ToLower(t)] {
			overlap++
		}
	}
	return float64(overlap) / float64(len(a)+len(b)-overlap)
}

func parseTags(jsonStr string) []string {
	var tags []string
	_ = json.Unmarshal([]byte(jsonStr), &tags)
	return tags
}

func mergeTags(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	merged := make([]string, 0, len(a)+len(b))
	for _, group := range [][]string{a, b} {
		for _, t := range group {
			lower := strings.ToLower(t)
			if seen[lower] {
				continue
			}
			seen[lower] = true
			merged = append(merged, t)
		}
	}
	return merged
}
