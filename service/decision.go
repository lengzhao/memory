// Package service provides memory decision engine for intelligent memory management.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
)

// DecisionType represents the decision for a memory candidate
type DecisionType string

const (
	DecisionAdd     DecisionType = "ADD"     // Add new memory
	DecisionUpdate  DecisionType = "UPDATE"  // Update existing memory
	DecisionDelete  DecisionType = "DELETE"  // Delete outdated memory
	DecisionIgnore  DecisionType = "IGNORE"  // Ignore this candidate
	DecisionMerge   DecisionType = "MERGE"   // Merge with existing memory
)

// MemoryDecision represents the decision for a single memory candidate
type MemoryDecision struct {
	Decision      DecisionType `json:"decision"`
	TargetID      string       `json:"target_id,omitempty"`      // Existing memory ID for UPDATE/DELETE/MERGE
	Reason        string       `json:"reason"`
	Confidence    float64      `json:"confidence"`
	MergedContent string       `json:"merged_content,omitempty"` // For MERGE: merged content
	MergedTitle   string       `json:"merged_title,omitempty"`   // For MERGE: merged title
	NewImportance int          `json:"new_importance,omitempty"` // Suggested importance after action
}

// DecisionRequest contains candidates and similar memories for decision making
type DecisionRequest struct {
	Candidates      []ExtractedMemory `json:"candidates"`
	SimilarMemories []SimilarMemory   `json:"similar_memories"`
	DialogContext   string            `json:"dialog_context"`
}

// SimilarMemory represents a potentially related existing memory
type SimilarMemory struct {
	ID          string  `json:"id"`
	Namespace   string  `json:"namespace"`
	Title       string  `json:"title"`
	Content     string  `json:"content"`
	Summary     string  `json:"summary"`
	Tags        []string `json:"tags"`
	Importance  int     `json:"importance"`
	Confidence  float64 `json:"confidence"`
	CreatedAt   string  `json:"created_at"`
	Similarity  float64 `json:"similarity"` // 0-1 similarity score
}

// DecisionResult contains all decisions
type DecisionResult struct {
	Decisions      []MemoryDecision `json:"decisions"`
	TotalTokens    int              `json:"total_tokens"`
	ProcessingTime int              `json:"processing_time_ms"`
}

// DecisionEngine handles intelligent memory decisions
type DecisionEngine struct {
	db *gorm.DB
}

// NewDecisionEngine creates a new decision engine
func NewDecisionEngine(db *gorm.DB) *DecisionEngine {
	return &DecisionEngine{db: db}
}

// FindSimilarMemories finds existing memories similar to the candidate using FTS5 BM25
func (de *DecisionEngine) FindSimilarMemories(ctx context.Context, candidate ExtractedMemory, topK int) ([]SimilarMemory, error) {
	if topK <= 0 {
		topK = 5
	}

	// Build search query from candidate content
	// Use title + content + tags for comprehensive matching
	searchQuery := buildFTSQuery(candidate.Title, candidate.Content, candidate.Tags)
	if searchQuery == "" {
		return nil, nil
	}

	// Use FTS5 with BM25 scoring for better relevance ranking
	// BM25 considers term frequency and document length
	var ftsResults []struct {
		ItemID    string
		BM25Score float64 // Lower is better for BM25 (0 = best match)
	}

	err := de.db.WithContext(ctx).Raw(`
		SELECT 
			fts_memory.item_id,
			bm25(fts_memory) as bm25_score
		FROM fts_memory 
		WHERE fts_memory MATCH ? 
		ORDER BY bm25_score
		LIMIT ?
	`, searchQuery, topK*2).Scan(&ftsResults).Error

	if err != nil {
		return nil, err
	}

	if len(ftsResults) == 0 {
		return nil, nil
	}

	// Fetch full items with scores
	itemIDs := make([]string, len(ftsResults))
	bm25Scores := make(map[string]float64)
	for i, r := range ftsResults {
		itemIDs[i] = r.ItemID
		bm25Scores[r.ItemID] = r.BM25Score
	}

	var items []model.MemoryItem
	err = de.db.WithContext(ctx).
		Where("id IN ? AND status = ?", itemIDs, model.ItemStatusActive).
		Find(&items).Error
	if err != nil {
		return nil, err
	}

	// Convert to SimilarMemory with combined similarity score
	var similar []SimilarMemory
	for _, item := range items {
		bm25Score := bm25Scores[item.ID]

		// BM25 score to similarity conversion
		// BM25: lower is better, typical range 0-10
		// Convert to 0-1 similarity where 1 is best
		ftsSim := bm25ToSimilarity(bm25Score)

		// Calculate tag overlap as additional signal
		tagSim := calculateTagOverlap(candidate.Tags, parseTags(item.TagsJSON))

		// Combined similarity: weighted average of FTS and tag similarity
		finalSim := 0.7*ftsSim + 0.3*tagSim

		if finalSim > 0.2 { // Lower threshold since BM25 is more accurate
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
	}

	// Sort by similarity descending
	sort.Slice(similar, func(i, j int) bool {
		return similar[i].Similarity > similar[j].Similarity
	})

	// Return topK
	if len(similar) > topK {
		similar = similar[:topK]
	}

	return similar, nil
}

// buildFTSQuery constructs a FTS5 search query from memory components
// Handles both English and Chinese text
func buildFTSQuery(title, content string, tags []string) string {
	var parts []string

	// Add title (higher weight, so include twice)
	if title != "" {
		cleanTitle := sanitizeFTSQuery(title)
		if cleanTitle != "" {
			parts = append(parts, cleanTitle)
		}
	}

	// Add content (extract first 200 runes to avoid query bloat)
	// Use runes to handle multi-byte characters (Chinese, emoji, etc.)
	if content != "" {
		runes := []rune(content)
		if len(runes) > 200 {
			content = string(runes[:200])
		}
		cleanContent := sanitizeFTSQuery(content)
		if cleanContent != "" {
			parts = append(parts, cleanContent)
		}
	}

	// Add tags with OR operator
	if len(tags) > 0 {
		var tagParts []string
		for _, tag := range tags {
			if tag != "" {
				tagParts = append(tagParts, sanitizeFTSQuery(tag))
			}
		}
		if len(tagParts) > 0 {
			parts = append(parts, strings.Join(tagParts, " OR "))
		}
	}

	return strings.Join(parts, " ")
}

// sanitizeFTSQuery cleans text for FTS5 query
// Removes FTS5 special characters that would cause syntax errors
func sanitizeFTSQuery(text string) string {
	// FTS5 special characters: " * ( ) - ~
	// Replace with space
	replacer := strings.NewReplacer(
		"\"", " ",
		"*", " ",
		"(", " ",
		")", " ",
		"-", " ",
		"~", " ",
		"\n", " ",
		"\t", " ",
	)

	cleaned := replacer.Replace(text)

	// Collapse multiple spaces
	for strings.Contains(cleaned, "  ") {
		cleaned = strings.ReplaceAll(cleaned, "  ", " ")
	}

	return strings.TrimSpace(cleaned)
}

// bm25ToSimilarity converts BM25 score to 0-1 similarity
// BM25: lower is better, 0 = perfect match
// Typical range: 0-10 for reasonable matches
func bm25ToSimilarity(bm25 float64) float64 {
	if bm25 <= 0 {
		return 1.0 // Perfect match
	}
	// Exponential decay: sim = exp(-bm25/3)
	// At bm25=0: sim=1, bm25=3: sim=0.37, bm25=6: sim=0.14
	sim := math.Exp(-bm25 / 3.0)
	if sim < 0 {
		return 0
	}
	if sim > 1 {
		return 1
	}
	return sim
}

// Decide uses LLM to make decisions for each candidate
func (de *DecisionEngine) Decide(ctx context.Context, llmConfig model.LLMConfig, req DecisionRequest) (*DecisionResult, error) {
	start := time.Now()

	// Build the decision prompt
	prompt := de.buildDecisionPrompt(req)

	// Call LLM
	decisions, tokens, err := de.callDecisionLLM(ctx, llmConfig, prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM decision failed: %w", err)
	}

	processingTime := int(time.Since(start).Milliseconds())

	return &DecisionResult{
		Decisions:      decisions,
		TotalTokens:    tokens,
		ProcessingTime: processingTime,
	}, nil
}

// buildDecisionPrompt creates the prompt for LLM decision making
func (de *DecisionEngine) buildDecisionPrompt(req DecisionRequest) string {
	prompt := `You are a memory management AI. Analyze the new memory candidates and existing similar memories to make optimal decisions.

DECISION RULES:
1. ADD: Create new memory if no similar existing memory covers the same topic
2. UPDATE: Replace existing memory if new info is more accurate/complete
3. DELETE: Mark existing as outdated if new info contradicts it
4. IGNORE: Skip if candidate is duplicate or irrelevant
5. MERGE: Combine with existing memory if they share the same topic (preserve all unique facts)

DECISION CRITERIA:
- Similarity > 0.8: Consider UPDATE, DELETE, or MERGE
- Same topic but different facts: MERGE
- Contradictory facts: DELETE old, ADD new OR UPDATE
- Duplicate: IGNORE
- Importance should be recalculated after merge/update

`

	// Add candidates
	prompt += "NEW CANDIDATES:\n"
	for i, c := range req.Candidates {
		prompt += fmt.Sprintf("\n[CANDIDATE %d]\n", i)
		prompt += fmt.Sprintf("TempID: %s\n", c.TempID)
		prompt += fmt.Sprintf("Namespace: %s\n", c.Namespace)
		prompt += fmt.Sprintf("Title: %s\n", c.Title)
		prompt += fmt.Sprintf("Content: %s\n", c.Content)
		prompt += fmt.Sprintf("Tags: %v\n", c.Tags)
		prompt += fmt.Sprintf("Importance: %d, Confidence: %.2f\n", c.Importance, c.Confidence)
	}

	// Add similar memories
	prompt += "\n\nEXISTING SIMILAR MEMORIES:\n"
	for i, s := range req.SimilarMemories {
		prompt += fmt.Sprintf("\n[MEMORY %d]\n", i)
		prompt += fmt.Sprintf("ID: %s\n", s.ID)
		prompt += fmt.Sprintf("Namespace: %s\n", s.Namespace)
		prompt += fmt.Sprintf("Title: %s\n", s.Title)
		prompt += fmt.Sprintf("Content: %s\n", s.Content)
		prompt += fmt.Sprintf("Tags: %v\n", s.Tags)
		prompt += fmt.Sprintf("Importance: %d, Similarity: %.2f\n", s.Importance, s.Similarity)
	}

	prompt += `\n\nReturn your decisions as JSON:
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

One decision per candidate, in the same order as candidates provided.`

	return prompt
}

// callDecisionLLM calls LLM for decision making
func (de *DecisionEngine) callDecisionLLM(ctx context.Context, cfg model.LLMConfig, prompt string) ([]MemoryDecision, int, error) {
	messages := []openAIMessage{
		{Role: "system", Content: "You are a precise memory management AI. Make decisions based on semantic similarity and factual overlap."},
		{Role: "user", Content: prompt},
	}

	apiResp, err := callLLM(ctx, cfg, messages, 0.2)
	if err != nil {
		return nil, 0, err
	}

	content := apiResp.Choices[0].Message.Content

	var result struct {
		Decisions []MemoryDecision `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, apiResp.Usage.TotalTokens, fmt.Errorf("failed to parse decisions: %w", err)
	}

	return result.Decisions, apiResp.Usage.TotalTokens, nil
}

// ExecuteDecisions executes the decisions and returns the results
func (de *DecisionEngine) ExecuteDecisions(ctx context.Context, candidates []ExtractedMemory, decisions []MemoryDecision) (*ExecutionResult, error) {
	result := &ExecutionResult{
		Added:    []string{},
		Updated:  []string{},
		Deleted:  []string{},
		Ignored:  []string{},
		Merged:   []string{},
	}

	if len(decisions) != len(candidates) {
		return nil, fmt.Errorf("decision count (%d) doesn't match candidate count (%d)", len(decisions), len(candidates))
	}

	for i, decision := range decisions {
		candidate := candidates[i]

		switch decision.Decision {
		case DecisionAdd:
			id, err := de.executeAdd(ctx, candidate)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("ADD failed for %s: %v", candidate.TempID, err))
			} else {
				result.Added = append(result.Added, id)
			}

		case DecisionUpdate:
			if decision.TargetID == "" {
				result.Errors = append(result.Errors, fmt.Sprintf("UPDATE missing target_id for %s", candidate.TempID))
				continue
			}
			err := de.executeUpdate(ctx, decision.TargetID, candidate, decision)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("UPDATE failed for %s: %v", candidate.TempID, err))
			} else {
				result.Updated = append(result.Updated, decision.TargetID)
			}

		case DecisionDelete:
			if decision.TargetID == "" {
				result.Errors = append(result.Errors, fmt.Sprintf("DELETE missing target_id for %s", candidate.TempID))
				continue
			}
			err := de.executeDelete(ctx, decision.TargetID)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("DELETE failed for %s: %v", candidate.TempID, err))
			} else {
				result.Deleted = append(result.Deleted, decision.TargetID)
			}

		case DecisionMerge:
			if decision.TargetID == "" {
				result.Errors = append(result.Errors, fmt.Sprintf("MERGE missing target_id for %s", candidate.TempID))
				continue
			}
			id, err := de.executeMerge(ctx, decision.TargetID, candidate, decision)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("MERGE failed for %s: %v", candidate.TempID, err))
			} else {
				result.Merged = append(result.Merged, id)
			}

		case DecisionIgnore:
			result.Ignored = append(result.Ignored, candidate.TempID)

		default:
			// Default to ADD if decision unclear
			id, err := de.executeAdd(ctx, candidate)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("default ADD failed for %s: %v", candidate.TempID, err))
			} else {
				result.Added = append(result.Added, id)
			}
		}
	}

	return result, nil
}

// ExecutionResult contains the results of decision execution
type ExecutionResult struct {
	Added   []string
	Updated []string
	Deleted []string
	Ignored []string
	Merged  []string
	Errors  []string
}

func (de *DecisionEngine) executeAdd(ctx context.Context, candidate ExtractedMemory) (string, error) {
	now := time.Now()
	item := model.MemoryItem{
		ID:            model.GenerateID(),
		Namespace:     fmt.Sprintf("%s/auto/%s", candidate.Namespace, time.Now().Format("20060102")),
		NamespaceType: candidate.Namespace,
		Title:         candidate.Title,
		Content:       candidate.Content,
		Summary:       candidate.Summary,
		TagsJSON:      toJSON(candidate.Tags),
		SourceType:    model.SourceTypeAgent,
		Importance:    candidate.Importance,
		Confidence:    candidate.Confidence,
		Status:        model.ItemStatusActive,
		CreatedAt:     now,
		UpdatedAt:     now,
		Version:       1,
	}

	if err := de.db.WithContext(ctx).Create(&item).Error; err != nil {
		return "", err
	}

	return item.ID, nil
}

func (de *DecisionEngine) executeUpdate(ctx context.Context, targetID string, candidate ExtractedMemory, decision MemoryDecision) error {
	updates := map[string]interface{}{
		"content":    candidate.Content,
		"title":      candidate.Title,
		"summary":    candidate.Summary,
		"tags_json":  toJSON(candidate.Tags),
		"updated_at": time.Now(),
		"version":    gorm.Expr("version + 1"),
	}

	if decision.NewImportance > 0 {
		updates["importance"] = decision.NewImportance
	}

	result := de.db.WithContext(ctx).Model(&model.MemoryItem{}).Where("id = ?", targetID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("target memory not found: %s", targetID)
	}

	return nil
}

func (de *DecisionEngine) executeDelete(ctx context.Context, targetID string) error {
	result := de.db.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("id = ?", targetID).
		Update("status", model.ItemStatusDeleted)

	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("target memory not found: %s", targetID)
	}

	return nil
}

func (de *DecisionEngine) executeMerge(ctx context.Context, targetID string, candidate ExtractedMemory, decision MemoryDecision) (string, error) {
	// Get existing memory
	var existing model.MemoryItem
	if err := de.db.WithContext(ctx).First(&existing, "id = ?", targetID).Error; err != nil {
		return "", err
	}

	// Determine merged content
	mergedTitle := decision.MergedTitle
	mergedContent := decision.MergedContent

	if mergedTitle == "" {
		mergedTitle = existing.Title
		if len(candidate.Title) > len(mergedTitle) {
			mergedTitle = candidate.Title
		}
	}

	if mergedContent == "" {
		// Simple merge: combine unique information
		mergedContent = de.mergeContent(existing.Content, candidate.Content)
	}

	// Merge tags
	mergedTags := mergeTags(parseTags(existing.TagsJSON), candidate.Tags)

	// Calculate new importance (take max or weighted average)
	newImportance := existing.Importance
	if decision.NewImportance > 0 {
		newImportance = decision.NewImportance
	} else if candidate.Importance > existing.Importance {
		newImportance = candidate.Importance
	}

	updates := map[string]interface{}{
		"title":       mergedTitle,
		"content":     mergedContent,
		"tags_json":   toJSON(mergedTags),
		"importance":  newImportance,
		"updated_at":  time.Now(),
		"version":     gorm.Expr("version + 1"),
		"confidence":  (existing.Confidence + candidate.Confidence) / 2,
	}

	if candidate.Summary != "" {
		updates["summary"] = candidate.Summary
	}

	result := de.db.WithContext(ctx).Model(&model.MemoryItem{}).Where("id = ?", targetID).Updates(updates)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected == 0 {
		return "", fmt.Errorf("target memory not found: %s", targetID)
	}

	// Create merge record for tracking
	mergeRecord := model.MemoryMerge{
		ID:              model.GenerateID(),
		TargetID:        targetID,
		SourceContent:   candidate.Content,
		SourceTitle:     candidate.Title,
		MergedContent:   mergedContent,
		MergedTitle:     mergedTitle,
		SourceDialogID:  "", // Will be set by caller if available
		CreatedAt:       time.Now(),
	}
	de.db.Create(&mergeRecord)

	return targetID, nil
}

// mergeContent intelligently merges two content strings
func (de *DecisionEngine) mergeContent(existing, new string) string {
	if existing == new {
		return existing
	}

	// If one contains the other, use the longer one
	if strings.Contains(existing, new) {
		return existing
	}
	if strings.Contains(new, existing) {
		return new
	}

	// Simple concatenation with separator
	// In production, this could use LLM to intelligently merge
	return existing + "\n\n[Additional Info]\n" + new
}

// Helper functions

func calculateTagOverlap(tags1, tags2 []string) float64 {
	if len(tags1) == 0 || len(tags2) == 0 {
		return 0
	}

	set1 := make(map[string]bool)
	for _, t := range tags1 {
		set1[strings.ToLower(t)] = true
	}

	overlap := 0
	for _, t := range tags2 {
		if set1[strings.ToLower(t)] {
			overlap++
		}
	}

	return float64(overlap) / float64(len(tags1)+len(tags2)-overlap)
}

func parseTags(jsonStr string) []string {
	var tags []string
	json.Unmarshal([]byte(jsonStr), &tags)
	return tags
}

func mergeTags(tags1, tags2 []string) []string {
	seen := make(map[string]bool)
	merged := []string{}

	for _, t := range tags1 {
		lower := strings.ToLower(t)
		if !seen[lower] {
			seen[lower] = true
			merged = append(merged, t)
		}
	}

	for _, t := range tags2 {
		lower := strings.ToLower(t)
		if !seen[lower] {
			seen[lower] = true
			merged = append(merged, t)
		}
	}

	return merged
}
