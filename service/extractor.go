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

// DefaultLLMTimeout bounds every LLM HTTP call when LLMConfig.TimeoutSeconds is unset.
const DefaultLLMTimeout = 120 * time.Second

// openAIRequest is the request body for OpenAI chat completion API.
type openAIRequest struct {
	Model          string                `json:"model"`
	Messages       []openAIMessage       `json:"messages"`
	Temperature    float64               `json:"temperature"`
	MaxTokens      int                   `json:"max_tokens,omitempty"`
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
//	    APIKey:   os.Getenv("OPENAI_API_KEY"),
//	    Model:    "gpt-4o",
//	}
//	result, err := service.QuickExtract(ctx, db, dialogText, cfg)
func QuickExtract(ctx context.Context, db *gorm.DB, scope Scope, dialogText string, llmCfg *model.LLMConfig) (*ExtractResult, error) {
	extractor := NewExtractor(db)
	return extractor.Extract(ctx, scope, ExtractRequest{
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

	// ExtractPolicy applies extra filtering after MinConfidence (e.g. transient ephemeral cues).
	ExtractPolicy *ExtractPolicy

	// RollSessionSummary asks the model to also refresh the session digest in the same call: the
	// previous digest is handed to it as input and the revised one is stored. It costs no extra
	// request, so a host that already extracts every turn can keep the digest current for free.
	// The digest is written before the memories are persisted, so it survives a persist failure.
	RollSessionSummary bool

	// previousSessionSummary is the stored digest handed back to the model for integration. It is
	// filled in by Extract from the session record; callers never set it.
	previousSessionSummary string
	// sessionSummaryRecord is the digest produced by this call, if any.
	sessionSummaryRecord *SessionSummary

	// PostExtractHook runs after MinConfidence and ExtractPolicy, before persistence.
	PostExtractHook func(ctx context.Context, mem []ExtractedMemory) ([]ExtractedMemory, error)
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

	// SessionSummary is the refreshed conversation digest (when RollSessionSummary was requested and
	// the session had one, or the model produced one).
	SessionSummary *SessionSummary `json:"session_summary_record,omitempty"`

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

// Extract processes dialog text and extracts memories.
//
// Caching: a completed extraction is reused for 48h, scoped by isolation identity (scope_key)
// and only when the request is a dry run. A cached row is never used to answer a request that
// actually persists, so a dry-run probe (or a retried write) cannot leave memories unwritten.
func (e *Extractor) Extract(ctx context.Context, scope Scope, req ExtractRequest) (*ExtractResult, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	start := time.Now()

	// Idempotency key: stable when only DialogText is set (backwards compatible)
	hash := extractionCacheKey(req)
	scopeKey := scope.String()

	// Check for existing extraction within 48 hours, for this isolation scope only.
	// Dry runs always call the model: they are probes and must not poison the cache either.
	if req.DryRun {
		// fall through to a fresh call
	} else {
		var existing model.DialogExtraction
		cutoff := time.Now().Add(-48 * time.Hour)
		err := e.db.WithContext(ctx).
			Where("scope_key = ? AND dialog_hash = ? AND created_at > ?", scopeKey, hash, cutoff).
			Order("created_at DESC").
			First(&existing).Error
		if err == nil {
			var memories []ExtractedMemory
			if existing.ExtractedMemoriesJSON != "" {
				_ = json.Unmarshal([]byte(existing.ExtractedMemoriesJSON), &memories)
			}
			filtered, perr := applyPostExtractPipeline(ctx, memories, req)
			if perr != nil {
				return nil, perr
			}
			cached := &ExtractResult{
				ExtractionID:   existing.ID,
				Status:         "cached",
				Memories:       filtered,
				TotalTokens:    valueOrZero(existing.TotalTokens),
				ProcessingTime: valueOrZero(existing.ProcessingTimeMs),
			}
			if req.RollSessionSummary {
				// Not a new digest (the model was not called), but the host should still see the
				// session's current narrative.
				current, serr := NewMemoryService(e.db).loadSessionSummary(ctx, scope)
				if serr == nil {
					cached.SessionSummary = current
				}
			}
			return cached, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("failed to query recent extraction cache: %w", err)
		}
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
	// Dry-run probes are recorded as their own status so they never satisfy a real cache lookup.
	initialStatus := model.ExtractionStatusProcessing
	if req.DryRun {
		initialStatus = model.ExtractionStatusPending
	}
	exRec := model.DialogExtraction{
		ID:         model.GenerateID(),
		ScopeKey:   scopeKey,
		DialogText: req.DialogText,
		DialogHash: hash,
		ConfigRef:  fmt.Sprintf("llm=%s;prompt=%s", llmConfigID, promptID),
		Status:     initialStatus,
		CreatedAt:  time.Now(),
	}
	if err := e.db.WithContext(ctx).Create(&exRec).Error; err != nil {
		// Backward compatibility: old databases may still have the legacy unique index on dialog_hash.
		// In this case, return the latest cached record for this hash (within this scope).
		if strings.Contains(err.Error(), "dialog_extractions.dialog_hash") {
			var legacy model.DialogExtraction
			if qErr := e.db.WithContext(ctx).
				Where("scope_key = ? AND dialog_hash = ?", scopeKey, hash).
				Order("created_at DESC").
				First(&legacy).Error; qErr == nil {
				var memories []ExtractedMemory
				if legacy.ExtractedMemoriesJSON != "" {
					_ = json.Unmarshal([]byte(legacy.ExtractedMemoriesJSON), &memories)
				}
				filtered, perr := applyPostExtractPipeline(ctx, memories, req)
				if perr != nil {
					return nil, perr
				}
				return &ExtractResult{
					ExtractionID:   legacy.ID,
					Status:         "cached",
					Memories:       filtered,
					TotalTokens:    valueOrZero(legacy.TotalTokens),
					ProcessingTime: valueOrZero(legacy.ProcessingTimeMs),
				}, nil
			}
		}
		return nil, fmt.Errorf("failed to create extraction record: %w", err)
	}

	if req.RollSessionSummary {
		existing, serr := NewMemoryService(e.db).loadSessionSummary(ctx, scope)
		if serr != nil {
			return nil, serr
		}
		if existing != nil {
			req.previousSessionSummary = existing.Summary
		}
	}

	memories, sessionSummaryText, tokens, err := e.callOpenAI(ctx, llmConfig, prompt, req)
	if err != nil {
		errMsg := err.Error()
		now := time.Now()
		exRec.ErrorMessage = &errMsg
		exRec.Status = model.ExtractionStatusFailed
		exRec.CompletedAt = &now
		e.db.WithContext(ctx).Save(&exRec)
		return nil, fmt.Errorf("LLM extraction failed: %w", err)
	}

	// Persist the digest before the memories: a failed persist below must not cost the session its
	// updated narrative, and a dry run still shows the host what the digest would be.
	if req.RollSessionSummary && strings.TrimSpace(sessionSummaryText) != "" {
		record, serr := NewMemoryService(e.db).writeSessionSummary(ctx, scope, sessionSummaryText, model.SessionSummarySourceLLM, -1)
		if serr != nil {
			GetLogger().Warn("failed to store session summary", "error", serr)
		} else {
			req.sessionSummaryRecord = record
		}
	}

	filtered, err := applyPostExtractPipeline(ctx, memories, req)
	if err != nil {
		errMsg := err.Error()
		now := time.Now()
		exRec.ErrorMessage = &errMsg
		exRec.Status = model.ExtractionStatusFailed
		exRec.CompletedAt = &now
		e.db.WithContext(ctx).Save(&exRec)
		return nil, fmt.Errorf("post-extract pipeline failed: %w", err)
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
	if req.DryRun {
		// Dry-run rows stay out of the completed cache: they were produced under a different
		// persistence decision and must be re-evaluated when the host writes for real.
		exRec.Status = model.ExtractionStatusPending
	} else {
		exRec.Status = model.ExtractionStatusCompleted
	}
	exRec.CompletedAt = &now

	if err := e.db.WithContext(ctx).Save(&exRec).Error; err != nil {
		return nil, fmt.Errorf("failed to update extraction record: %w", err)
	}

	// If not dry run, persist memories to database
	// A dry run still reports the digest it produced: the host is previewing what the model would do,
	// and the digest is part of that.
	if req.DryRun && req.sessionSummaryRecord == nil && req.RollSessionSummary {
		if current, serr := NewMemoryService(e.db).loadSessionSummary(ctx, scope); serr == nil {
			req.sessionSummaryRecord = current
		}
	}

	var decisionSummary *DecisionExecutionSummary
	if !req.DryRun {
		if req.UseDecisionEngine {
			// New flow: extract → find similar → decide → persist
			summary, err := e.persistWithDecisionEngine(ctx, scope, filtered, req)
			if err != nil {
				logger.WarnContext(ctx, "decision engine persist failed, falling back to simple persist", "error", err)
				// Fallback to simple persist
				for _, mem := range filtered {
					if err := e.persistMemory(ctx, scope, mem); err != nil {
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
				if err := e.persistMemory(ctx, scope, mem); err != nil {
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
		SessionSummary: req.sessionSummaryRecord,
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

	// A zero/negative timeout means "no timeout" for http.Client, which would let a stuck
	// provider block a caller forever. Fall back to a bounded default instead.
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultLLMTimeout
	}
	httpClient := &http.Client{
		Timeout: timeout,
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
// callOpenAI extracts memories from dialog, and the session digest when it was requested.
//
// Both come back from the same request: rolling the digest is a field in the response, not a second
// API call, so keeping a session summary current costs nothing beyond a few output tokens.
func (e *Extractor) callOpenAI(ctx context.Context, cfg model.LLMConfig, prompt model.ExtractionPrompt, req ExtractRequest) ([]ExtractedMemory, string, int, error) {
	userPrompt := buildExtractionUserPrompt(req, req.previousSessionSummary)
	messages := []openAIMessage{
		{Role: "system", Content: prompt.SystemPrompt},
		{Role: "user", Content: userPrompt},
	}

	apiResp, err := callLLM(ctx, cfg, messages, cfg.Temperature)
	if err != nil {
		return nil, "", 0, err
	}

	content := apiResp.Choices[0].Message.Content

	// Expected shape: {"memories": [...], "session_summary": "..."}.
	//
	// The envelope is accepted whenever it is a JSON object carrying either key: a turn can legitimately
	// yield no memories while still producing a session digest ("memories": []), and treating that as
	// a parse failure would silently drop the digest.
	var envelope struct {
		Memories       *[]ExtractedMemory `json:"memories"`
		SessionSummary *string            `json:"session_summary"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err == nil && (envelope.Memories != nil || envelope.SessionSummary != nil) {
		memories := []ExtractedMemory{}
		if envelope.Memories != nil {
			memories = *envelope.Memories
		}
		summary := ""
		if envelope.SessionSummary != nil {
			summary = strings.TrimSpace(*envelope.SessionSummary)
		}
		return memories, summary, apiResp.Usage.TotalTokens, nil
	}

	// A model that emitted a bare array of memories, or a single memory object.
	var memories []ExtractedMemory
	if err := json.Unmarshal([]byte(content), &memories); err != nil {
		var singleMemory ExtractedMemory
		if err := json.Unmarshal([]byte(content), &singleMemory); err != nil {
			return nil, "", apiResp.Usage.TotalTokens, fmt.Errorf("failed to parse LLM output as memories: %w", err)
		}
		memories = []ExtractedMemory{singleMemory}
	}

	return memories, "", apiResp.Usage.TotalTokens, nil
}

// persistMemory saves extracted memory to database
// persistMemory writes one extracted memory. Duplicate detection is expressed as a content hash
// (DedupeKey) rather than a read-then-write, so concurrent extractions of the same dialog cannot
// both insert the same fact.
func (e *Extractor) persistMemory(ctx context.Context, scope Scope, mem ExtractedMemory) error {
	key := contentDedupeKey(mem)
	_, err := NewMemoryService(e.db).Remember(ctx, scope, RememberRequest{
		NamespaceType: mem.Namespace,
		Title:         mem.Title,
		Content:       mem.Content,
		Summary:       mem.Summary,
		Tags:          mem.Tags,
		SourceType:    model.SourceTypeAgent,
		Importance:    mem.Importance,
		Confidence:    mem.Confidence,
		DedupeKey:     &key,
	})
	return err
}

// contentDedupeKey identifies a memory by its scope-independent content, which is what makes
// extraction idempotent without a race-prone lookup.
func contentDedupeKey(mem ExtractedMemory) string {
	sum := sha256.Sum256([]byte(string(mem.Namespace) + "\x00" + mem.Title + "\x00" + mem.Content))
	return "content:" + hex.EncodeToString(sum[:16])
}

// persistWithDecisionEngine uses the new flow: extract → find similar → decide → persist
func (e *Extractor) persistWithDecisionEngine(ctx context.Context, scope Scope, memories []ExtractedMemory, req ExtractRequest) (*DecisionExecutionSummary, error) {
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
		similar, err := decisionEngine.FindSimilarMemories(ctx, scope, mem, topK)
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
	execResult, err := decisionEngine.ExecuteDecisions(ctx, scope, memories, decisionResult.Decisions)
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
		if cfg.Model == "" || cfg.APIKey == "" {
			return model.LLMConfig{}, "", fmt.Errorf("invalid LLM config: model/api_key are required")
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
		return cfg, fmt.Sprintf("runtime:openai:%s", cfg.Model), nil
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
			p.ID = "prompt-default-v3-code"
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
func buildExtractionUserPrompt(req ExtractRequest, previousSessionSummary string) string {
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

	if req.RollSessionSummary {
		sb.WriteString("\n## SESSION SUMMARY REQUEST\n")
		sb.WriteString("A separate system keeps the running digest of this conversation. Return an updated digest in\n")
		sb.WriteString("the \"session_summary\" field: what the user is working on, decisions already made, constraints\n")
		sb.WriteString("and open questions. Integrate the previous digest below with the new dialog instead of\n")
		sb.WriteString("restating only the newest message. Keep it to a few sentences, in the language of the dialog.\n")
		if strings.TrimSpace(previousSessionSummary) != "" {
			sb.WriteString("\n## Previous session summary (integrate this)\n")
			sb.WriteString(strings.TrimSpace(previousSessionSummary))
			sb.WriteString("\n")
		} else {
			sb.WriteString("\n(There is no previous digest: this is the first one.)\n")
		}
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
