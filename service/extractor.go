// Package service provides memory extraction services using LLM.
package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
)

// openAIRequest is the request body for OpenAI chat completion API.
type openAIRequest struct {
	Model          string            `json:"model"`
	Messages       []openAIMessage   `json:"messages"`
	Temperature    float64           `json:"temperature"`
	MaxTokens      int               `json:"max_tokens,omitempty"`
	ResponseFormat *openAIResponseFormat `json:"response_format,omitempty"`
}

type openAIResponseFormat struct {
	Type string `json:"type"` // "json_object" for JSON mode
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openAIResponse is a response from the OpenAI API.
type openAIResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int           `json:"index"`
		Message      openAIMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *openAIError `json:"error,omitempty"`
}

type openAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func (e openAIError) Error() string {
	return fmt.Sprintf("OpenAI API error: %s (type: %s, code: %s)", e.Message, e.Type, e.Code)
}

// Extractor handles dialog to memory extraction
type Extractor struct {
	db *gorm.DB
}

// NewExtractor creates a new extractor instance
func NewExtractor(db *gorm.DB) *Extractor {
	return &Extractor{db: db}
}

// QuickExtract is a convenience function for code-only extraction without DB setup.
// Useful for simple scripts or when you want to pass config directly each time.
// Example:
//
//	cfg := &model.LLMConfig{
//	    Provider: model.LLMProviderOpenAI,
//	    APIKey:   os.Getenv("OPENAI_API_KEY"),
//	    Model:    "gpt-4o",
//	}
//	result, err := service.QuickExtract(ctx, db, dialogText, cfg)
func QuickExtract(ctx context.Context, db *gorm.DB, dialogText string, llmCfg *model.LLMConfig) (*ExtractResult, error) {
	extractor := NewExtractor(db)
	return extractor.Extract(ctx, ExtractRequest{
		DialogText:       dialogText,
		LLMConfig:        llmCfg,
		ExtractionPrompt: nil, // Use builtin
		MinConfidence:    0.7,
	})
}

// ExtractRequest represents a dialog extraction request
type ExtractRequest struct {
	DialogText        string
	LLMConfigID       string // deprecated: database lookup is no longer supported
	PromptID          string // deprecated: database lookup is no longer supported
	ContextMemories   []string
	MinConfidence     float64 // default 0.7
	DryRun            bool
	UseDecisionEngine bool // default false, if true uses new extract → similar → decide → persist flow
	SimilarTopK       int  // default 5, number of similar memories to fetch per candidate

	// ReferenceTime is the instant treated as "now" for resolving relative time (e.g. 明天, next week).
	// If nil, Extract uses the wall-clock time when the request runs.
	ReferenceTime *time.Time
	// TimeZone is an IANA name (e.g. "Asia/Shanghai", "America/New_York") for presenting reference
	// instants in the model prompt. Empty = use the location of ReferenceTime (or local wall clock).
	TimeZone string
	// ResolutionContext is optional free text: user display name, who 他/她/经理 refers to, session
	// participants, etc. The model is instructed to replace vague references in stored title/content/summary.
	ResolutionContext string

	// LLMConfig must be provided directly in code.
	LLMConfig *model.LLMConfig

	// ExtractionPrompt allows passing prompt directly in code.
	// If nil, builtin default prompt is used.
	ExtractionPrompt *model.ExtractionPrompt
}

// ExtractedMemory represents a single extracted memory item
type ExtractedMemory struct {
	TempID       string                 `json:"temp_id"`
	Namespace    model.NamespaceType    `json:"namespace"`
	Title        string                 `json:"title"`
	Content      string                 `json:"content"`
	Summary      string                 `json:"summary,omitempty"`
	Tags         []string               `json:"tags,omitempty"`
	Importance   int                    `json:"importance"`
	Confidence   float64                `json:"confidence"`
	Reasoning    string                 `json:"reasoning,omitempty"`
	TaskMetadata map[string]interface{} `json:"task_metadata,omitempty"`
}

// ExtractResult represents the extraction result
type ExtractResult struct {
	ExtractionID   string
	Status         string
	Memories       []ExtractedMemory
	TotalTokens    int
	ProcessingTime int // ms

	// Decision engine results (when UseDecisionEngine is true)
	DecisionResult *DecisionExecutionSummary `json:"decision_result,omitempty"`
}

// DecisionExecutionSummary contains the results of decision-based persistence
type DecisionExecutionSummary struct {
	Added   []string `json:"added"`
	Updated []string `json:"updated"`
	Deleted []string `json:"deleted"`
	Ignored []string `json:"ignored"`
	Merged  []string `json:"merged"`
	Errors  []string `json:"errors,omitempty"`
}

// Extract processes dialog text and extracts memories
// In production, this would call actual LLM API
func (e *Extractor) Extract(ctx context.Context, req ExtractRequest) (*ExtractResult, error) {
	start := time.Now()

	// Idempotency key: stable when only DialogText is set (backwards compatible)
	hash := extractionCacheKey(req)

	// Check for existing extraction within 48 hours
	var existing model.DialogExtraction
	cutoff := time.Now().Add(-48 * time.Hour)
	err := e.db.WithContext(ctx).Where("dialog_hash = ? AND created_at > ?", hash, cutoff).First(&existing).Error
	if err == nil {
		// Return cached result
		var memories []ExtractedMemory
		if existing.ExtractedMemoriesJSON != "" {
			json.Unmarshal([]byte(existing.ExtractedMemoriesJSON), &memories)
		}
		return &ExtractResult{
			ExtractionID:   existing.ID,
			Status:         "cached",
			Memories:       memories,
			TotalTokens:    valueOrZero(existing.TotalTokens),
			ProcessingTime: valueOrZero(existing.ProcessingTimeMs),
		}, nil
	}

	// Resolve LLM config: use provided, or lookup from DB, or fail
	llmConfig, llmConfigID, err := e.resolveLLMConfig(ctx, req)
	if err != nil {
		return nil, err
	}

	// Resolve prompt from request or builtin default
	prompt, promptID, err := e.resolveExtractionPromptV2(req)
	if err != nil {
		return nil, err
	}

	// Create extraction record (even with code config, we record for audit)
	exRec := model.DialogExtraction{
		ID:          model.GenerateID(),
		DialogText:  req.DialogText,
		DialogHash:  hash,
		ConfigRef:   fmt.Sprintf("llm=%s;prompt=%s", llmConfigID, promptID),
		Status:      model.ExtractionStatusProcessing,
		CreatedAt:   time.Now(),
	}
	if err := e.db.WithContext(ctx).Create(&exRec).Error; err != nil {
		return nil, fmt.Errorf("failed to create extraction record: %w", err)
	}

	memories, tokens, err := e.callOpenAI(ctx, llmConfig, prompt, req)
	if err != nil {
		errMsg := err.Error()
		now := time.Now()
		exRec.ErrorMessage = &errMsg
		exRec.Status = model.ExtractionStatusFailed
		exRec.CompletedAt = &now
		e.db.WithContext(ctx).Save(&exRec)
		return nil, fmt.Errorf("LLM extraction failed: %w", err)
	}

	// Filter by confidence
	minConf := req.MinConfidence
	if minConf == 0 {
		minConf = 0.7
	}
	var filtered []ExtractedMemory
	for _, m := range memories {
		if m.Confidence >= minConf {
			filtered = append(filtered, m)
		}
	}

	// Serialize results
	memoriesJSON, _ := json.Marshal(filtered)

	// Calculate metrics using actual tokens from API
	processingTime := int(time.Since(start).Milliseconds())
	totalTokens := tokens

	now := time.Now()
	exRec.ExtractedMemoriesJSON = string(memoriesJSON)
	exRec.TotalTokens = &totalTokens
	exRec.ProcessingTimeMs = &processingTime
	exRec.Status = model.ExtractionStatusCompleted
	exRec.CompletedAt = &now

	if err := e.db.WithContext(ctx).Save(&exRec).Error; err != nil {
		return nil, fmt.Errorf("failed to update extraction record: %w", err)
	}

	// If not dry run, persist memories to database
	var decisionSummary *DecisionExecutionSummary
	if !req.DryRun {
		if req.UseDecisionEngine {
			// New flow: extract → find similar → decide → persist
			summary, err := e.persistWithDecisionEngine(ctx, filtered, req)
			if err != nil {
				logger.WarnContext(ctx, "decision engine persist failed, falling back to simple persist", "error", err)
				// Fallback to simple persist
				for _, mem := range filtered {
					if err := e.persistMemory(ctx, mem); err != nil {
						logger.WarnContext(ctx, "fallback persist failed", "error", err)
					}
				}
			} else {
				decisionSummary = summary
			}
		} else {
			// Simple persist (original behavior)
			persisted := 0
			for _, mem := range filtered {
				if err := e.persistMemory(ctx, mem); err != nil {
					logger.WarnContext(ctx, "failed to persist memory", "title", mem.Title, "error", err)
				} else {
					persisted++
				}
			}
			logger.InfoContext(ctx, "memories persisted", "total", len(filtered), "succeeded", persisted)
		}
	}

	return &ExtractResult{
		ExtractionID:   exRec.ID,
		Status:         string(exRec.Status),
		Memories:       filtered,
		TotalTokens:    totalTokens,
		ProcessingTime: processingTime,
		DecisionResult: decisionSummary,
	}, nil
}

// callLLM calls the OpenAI-compatible API and returns the raw response.
// This is the generic HTTP wrapper used by both extraction and decision engines.
func callLLM(ctx context.Context, cfg model.LLMConfig, messages []openAIMessage, temperature float64) (*openAIResponse, error) {
	baseURL := "https://api.openai.com/v1"
	if cfg.BaseURL != nil && *cfg.BaseURL != "" {
		baseURL = *cfg.BaseURL
	}
	url := baseURL + "/chat/completions"

	reqBody := openAIRequest{
		Model:          cfg.Model,
		Messages:       messages,
		Temperature:    temperature,
		MaxTokens:      cfg.MaxTokens,
		ResponseFormat: &openAIResponseFormat{Type: "json_object"},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	apiKey := cfg.APIKey
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{
		Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second,
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr openAIResponse
		if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error != nil {
			return nil, apiErr.Error
		}
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var apiResp openAIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if apiResp.Error != nil {
		return nil, apiResp.Error
	}

	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("API returned no choices")
	}

	return &apiResp, nil
}

// callOpenAI calls the OpenAI API to extract memories from dialog
// Returns extracted memories, total token count, and error if any
func (e *Extractor) callOpenAI(ctx context.Context, cfg model.LLMConfig, prompt model.ExtractionPrompt, req ExtractRequest) ([]ExtractedMemory, int, error) {
	userPrompt := buildExtractionUserPrompt(req)
	messages := []openAIMessage{
		{Role: "system", Content: prompt.SystemPrompt},
		{Role: "user", Content: userPrompt},
	}

	apiResp, err := callLLM(ctx, cfg, messages, cfg.Temperature)
	if err != nil {
		return nil, 0, err
	}

	content := apiResp.Choices[0].Message.Content

	// The response should be a JSON object with a "memories" key or directly an array
	// Try to parse as object first (expected format: {"memories": [...]})
	var result struct {
		Memories []ExtractedMemory `json:"memories"`
	}
	if err := json.Unmarshal([]byte(content), &result); err == nil && len(result.Memories) > 0 {
		return result.Memories, apiResp.Usage.TotalTokens, nil
	}

	// Try to parse as direct array
	var memories []ExtractedMemory
	if err := json.Unmarshal([]byte(content), &memories); err != nil {
		// If both fail, try to wrap in an array
		var singleMemory ExtractedMemory
		if err := json.Unmarshal([]byte(content), &singleMemory); err != nil {
			return nil, apiResp.Usage.TotalTokens, fmt.Errorf("failed to parse LLM output as memories: %w", err)
		}
		memories = []ExtractedMemory{singleMemory}
	}

	return memories, apiResp.Usage.TotalTokens, nil
}

// persistMemory saves extracted memory to database
func (e *Extractor) persistMemory(ctx context.Context, mem ExtractedMemory) error {
	// Scope duplicate checks to the resolved namespace to avoid cross-session leakage.
	ns := buildDefaultNamespace(mem.Namespace)
	if isIsolationEnabled(ctx) {
		meta, err := IsolationFromContext(ctx)
		if err != nil {
			return err
		}
		ns = buildNamespace(meta, mem.Namespace)
	}

	var existing model.MemoryItem
	err := e.db.WithContext(ctx).
		Where("namespace = ? AND namespace_type = ? AND content = ?", ns, mem.Namespace, mem.Content).
		First(&existing).Error
	if err == nil {
		// Duplicate found, skip
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	svc := NewMemoryService(e.db)
	_, err = svc.Remember(ctx, RememberRequest{
		NamespaceType: mem.Namespace,
		Title:         mem.Title,
		Content:       mem.Content,
		Summary:       mem.Summary,
		Tags:          mem.Tags,
		SourceType:    model.SourceTypeAgent,
		Importance:    mem.Importance,
		Confidence:    mem.Confidence,
	})
	return err
}

// persistWithDecisionEngine uses the new flow: extract → find similar → decide → persist
func (e *Extractor) persistWithDecisionEngine(ctx context.Context, memories []ExtractedMemory, req ExtractRequest) (*DecisionExecutionSummary, error) {
	if len(memories) == 0 {
		return &DecisionExecutionSummary{}, nil
	}

	topK := req.SimilarTopK
	if topK <= 0 {
		topK = 5
	}

	// Reuse the same config resolution logic
	llmConfig, _, err := e.resolveLLMConfig(ctx, req)
	if err != nil {
		return nil, err
	}

	// Initialize decision engine
	decisionEngine := NewDecisionEngine(e.db)

	// Step 1: Find similar memories for all candidates
	allSimilarMemories := make(map[string][]SimilarMemory)
	for _, mem := range memories {
		similar, err := decisionEngine.FindSimilarMemories(ctx, mem, topK)
		if err != nil {
			logger.WarnContext(ctx, "failed to find similar memories", "temp_id", mem.TempID, "error", err)
			continue
		}
		allSimilarMemories[mem.TempID] = similar
	}

	// Step 2: Build decision request
	dedupSimilar := deduplicateSimilarMemories(allSimilarMemories)
	decisionReq := DecisionRequest{
		Candidates:      memories,
		SimilarMemories: dedupSimilar,
		DialogContext:   req.DialogText,
	}

	// Step 3: Call LLM to make decisions
	decisionResult, err := decisionEngine.Decide(ctx, llmConfig, decisionReq)
	if err != nil {
		return nil, fmt.Errorf("decision engine failed: %w", err)
	}

	// Step 4: Execute decisions
	execResult, err := decisionEngine.ExecuteDecisions(ctx, memories, decisionResult.Decisions)
	if err != nil {
		return nil, fmt.Errorf("failed to execute decisions: %w", err)
	}

	return &DecisionExecutionSummary{
		Added:   execResult.Added,
		Updated: execResult.Updated,
		Deleted: execResult.Deleted,
		Ignored: execResult.Ignored,
		Merged:  execResult.Merged,
		Errors:  execResult.Errors,
	}, nil
}

// deduplicateSimilarMemories removes duplicate similar memories across candidates
func deduplicateSimilarMemories(allSimilar map[string][]SimilarMemory) []SimilarMemory {
	seen := make(map[string]bool)
	var dedup []SimilarMemory

	for _, similarList := range allSimilar {
		for _, sim := range similarList {
			if !seen[sim.ID] {
				seen[sim.ID] = true
				dedup = append(dedup, sim)
			}
		}
	}

	return dedup
}

// resolveLLMConfig resolves runtime LLM config from request.
// Returns the config, a recordable ID, and error.
func (e *Extractor) resolveLLMConfig(_ context.Context, req ExtractRequest) (model.LLMConfig, string, error) {
	if req.LLMConfig != nil {
		cfg := *req.LLMConfig
		if cfg.Provider == "" || cfg.Model == "" || cfg.APIKey == "" {
			return model.LLMConfig{}, "", fmt.Errorf("invalid LLM config: provider/model/api_key are required")
		}
		if cfg.MaxTokens <= 0 {
			cfg.MaxTokens = 4096
		}
		if cfg.Temperature == 0 {
			cfg.Temperature = 0.3
		}
		if cfg.TimeoutSeconds <= 0 {
			cfg.TimeoutSeconds = 30
		}
		return cfg, fmt.Sprintf("runtime:%s:%s", cfg.Provider, cfg.Model), nil
	}

	if req.LLMConfigID != "" {
		return model.LLMConfig{}, "", fmt.Errorf("LLMConfigID is deprecated; pass ExtractRequest.LLMConfig directly")
	}

	return model.LLMConfig{}, "", fmt.Errorf("missing LLM config: pass ExtractRequest.LLMConfig")
}

// resolveExtractionPromptV2 resolves prompt from request or builtin default.
// Returns the prompt, the ID to record, and error.
func (e *Extractor) resolveExtractionPromptV2(req ExtractRequest) (model.ExtractionPrompt, string, error) {
	if req.ExtractionPrompt != nil {
		p := *req.ExtractionPrompt
		if p.ID == "" {
			p.ID = "prompt-default-v1-code"
		}
		if strings.TrimSpace(p.SystemPrompt) == "" {
			return model.ExtractionPrompt{}, "", fmt.Errorf("invalid ExtractionPrompt: system prompt is required")
		}
		return p, p.ID, nil
	}

	if req.PromptID != "" {
		return model.ExtractionPrompt{}, "", fmt.Errorf("PromptID is deprecated; pass ExtractRequest.ExtractionPrompt directly")
	}

	builtin := BuiltinExtractionPrompt()
	return builtin, builtin.ID, nil
}

// Helper functions

// extractionCacheKey hashes the request for idempotency. If only DialogText is set (legacy),
// the key matches the former calculateHash(DialogText) result.
func extractionCacheKey(req ExtractRequest) string {
	if req.ReferenceTime == nil && req.TimeZone == "" && req.ResolutionContext == "" {
		return calculateHash(req.DialogText)
	}
	var b strings.Builder
	b.WriteString(req.DialogText)
	b.WriteByte(0)
	if req.ReferenceTime != nil {
		b.WriteString(req.ReferenceTime.UTC().Format(time.RFC3339Nano))
	}
	b.WriteByte(0)
	b.WriteString(req.TimeZone)
	b.WriteByte(0)
	b.WriteString(req.ResolutionContext)
	h := sha256.New()
	h.Write([]byte(b.String()))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func calculateHash(text string) string {
	h := sha256.New()
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))[:32] // First 16 bytes
}

// buildExtractionUserPrompt injects reference time, optional time zone, and entity context.
// These runtime values are combined with the system prompt (defined in extraction_defaults.go)
// which already contains the classification rules and output format.
func buildExtractionUserPrompt(req ExtractRequest) string {
	ref := time.Now()
	if req.ReferenceTime != nil {
		ref = *req.ReferenceTime
	}
	loc := ref.Location()
	if req.TimeZone != "" {
		if l, err := time.LoadLocation(req.TimeZone); err == nil {
			loc = l
		}
	}
	t := ref.In(loc)

	var sb strings.Builder
	sb.WriteString("## Reference (for resolving relative time — use for 明天, 下周五, next week, etc.)\n")
	sb.WriteString(fmt.Sprintf("- Reference instant: %s\n", t.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("- Date (local): %s (%s)\n", t.Format("2006-01-02"), t.Weekday().String()))
	if req.TimeZone != "" {
		sb.WriteString(fmt.Sprintf("- Time zone: %s\n", req.TimeZone))
	}

	if strings.TrimSpace(req.ResolutionContext) != "" {
		sb.WriteString("\n## Entity context (for disambiguating 他/她/经理, etc.)\n")
		sb.WriteString(strings.TrimSpace(req.ResolutionContext))
		sb.WriteString("\n")
	}

	sb.WriteString("\n## Dialog to analyze\n")
	sb.WriteString("\"\"\"\n")
	sb.WriteString(req.DialogText)
	sb.WriteString("\n\"\"\"\n")

	return sb.String()
}


func toJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func valueOrZero(ptr *int) int {
	if ptr == nil {
		return 0
	}
	return *ptr
}

