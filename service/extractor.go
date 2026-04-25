// Package service provides memory extraction services using LLM.
package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
)

// OpenAIRequest represents the request body for OpenAI chat completion API
type OpenAIRequest struct {
	Model       string          `json:"model"`
	Messages    []OpenAIMessage `json:"messages"`
	Temperature float64         `json:"temperature"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

type ResponseFormat struct {
	Type string `json:"type"` // "json_object" for JSON mode
}

type OpenAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OpenAIResponse represents the response from OpenAI API
type OpenAIResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int         `json:"index"`
		Message      OpenAIMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *OpenAIError `json:"error,omitempty"`
}

type OpenAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func (e OpenAIError) Error() string {
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

// ExtractRequest represents a dialog extraction request
type ExtractRequest struct {
	DialogText      string
	LLMConfigID     string // empty = use default
	PromptID        string // empty = use default
	ContextMemories []string
	MinConfidence   float64 // default 0.7
	DryRun          bool
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
	CostEstimate   float64
	ProcessingTime int // ms
}

// Extract processes dialog text and extracts memories
// In production, this would call actual LLM API
func (e *Extractor) Extract(ctx context.Context, req ExtractRequest) (*ExtractResult, error) {
	start := time.Now()

	// Calculate dialog hash for idempotency
	hash := calculateHash(req.DialogText)

	// Check for existing extraction within 48 hours
	var existing model.DialogExtraction
	cutoff := time.Now().Add(-48 * time.Hour)
	err := e.db.Where("dialog_hash = ? AND created_at > ?", hash, cutoff).First(&existing).Error
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
			CostEstimate:   valueOrZeroF(existing.CostEstimate),
			ProcessingTime: valueOrZero(existing.ProcessingTimeMs),
		}, nil
	}

	// Get default LLM config if not specified
	llmConfigID := req.LLMConfigID
	if llmConfigID == "" {
		var cfg model.LLMConfig
		if err := e.db.Where("is_default = ?", true).First(&cfg).Error; err != nil {
			return nil, fmt.Errorf("no default LLM config found: %w", err)
		}
		llmConfigID = cfg.ID
	}

	// Get default prompt if not specified
	promptID := req.PromptID
	if promptID == "" {
		var prompt model.ExtractionPrompt
		if err := e.db.Where("is_default = ?", true).First(&prompt).Error; err != nil {
			return nil, fmt.Errorf("no default extraction prompt found: %w", err)
		}
		promptID = prompt.ID
	}

	// Create extraction record
	extraction := model.DialogExtraction{
		ID:            model.GenerateID(),
		DialogText:    req.DialogText,
		DialogHash:    hash,
		LLMConfigID:   llmConfigID,
		PromptID:      promptID,
		Status:        model.ExtractionStatusProcessing,
		CreatedAt:     time.Now(),
	}
	if err := e.db.Create(&extraction).Error; err != nil {
		return nil, fmt.Errorf("failed to create extraction record: %w", err)
	}

	// Load LLM config and prompt for API call
	var llmConfig model.LLMConfig
	if err := e.db.First(&llmConfig, llmConfigID).Error; err != nil {
		return nil, fmt.Errorf("failed to load LLM config: %w", err)
	}

	var prompt model.ExtractionPrompt
	if err := e.db.First(&prompt, promptID).Error; err != nil {
		return nil, fmt.Errorf("failed to load prompt: %w", err)
	}

	// Call OpenAI API for real extraction
	memories, tokens, err := e.callOpenAI(ctx, llmConfig, prompt, req.DialogText)
	if err != nil {
		// Update extraction record with error
		errMsg := err.Error()
		now := time.Now()
		extraction.ErrorMessage = &errMsg
		extraction.Status = model.ExtractionStatusFailed
		extraction.CompletedAt = &now
		e.db.Save(&extraction)
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
	// Cost estimate: $0.01 per 1K tokens for GPT-4o (rough estimate)
	costEstimate := float64(totalTokens) * 0.000005

	now := time.Now()
	extraction.ExtractedMemoriesJSON = string(memoriesJSON)
	extraction.TotalTokens = &totalTokens
	extraction.CostEstimate = &costEstimate
	extraction.ProcessingTimeMs = &processingTime
	extraction.Status = model.ExtractionStatusCompleted
	extraction.CompletedAt = &now

	if err := e.db.Save(&extraction).Error; err != nil {
		return nil, fmt.Errorf("failed to update extraction record: %w", err)
	}

	// If not dry run, persist memories to database
	if !req.DryRun {
		for _, mem := range filtered {
			if err := e.persistMemory(ctx, mem); err != nil {
				// Log error but continue
				fmt.Printf("Failed to persist memory: %v\n", err)
			}
		}
	}

	return &ExtractResult{
		ExtractionID:   extraction.ID,
		Status:         string(extraction.Status),
		Memories:       filtered,
		TotalTokens:    totalTokens,
		CostEstimate:   costEstimate,
		ProcessingTime: processingTime,
	}, nil
}

// callOpenAI calls the OpenAI API to extract memories from dialog
// Returns extracted memories, total token count, and error if any
func (e *Extractor) callOpenAI(ctx context.Context, cfg model.LLMConfig, prompt model.ExtractionPrompt, dialog string) ([]ExtractedMemory, int, error) {
	// Build API URL
	baseURL := "https://api.openai.com/v1"
	if cfg.BaseURL != nil && *cfg.BaseURL != "" {
		baseURL = *cfg.BaseURL
	}
	url := baseURL + "/chat/completions"

	// Build user prompt with dialog
	userPrompt := fmt.Sprintf(`Analyze the following dialog and extract valuable memories.

CLASSIFICATION:
- transient: Temporary conversation context, short-lived
- profile: User preferences and personal info, long-term stable
- action: Tasks, todos, actionable items with goals or deadlines
- knowledge: Learned facts, skills, methods, procedures

Dialog:
"""
%s
"""

Extract memories and return them as: {"memories": [...]}

Each memory must have: namespace (transient/profile/action/knowledge), title, content, summary, tags, importance (0-100), confidence (0-1), reasoning.`, dialog)

	// Build request body
	reqBody := OpenAIRequest{
		Model:    cfg.Model,
		Messages: []OpenAIMessage{
			{Role: "system", Content: prompt.SystemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: cfg.Temperature,
		MaxTokens:   cfg.MaxTokens,
		ResponseFormat: &ResponseFormat{Type: "json_object"},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	// Note: API key is used as-is. Caller is responsible for decryption if needed.
	apiKey := cfg.APIKey
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	// Execute request with timeout
	httpClient := &http.Client{
		Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second,
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read response: %w", err)
	}

	// Check HTTP status
	if resp.StatusCode != http.StatusOK {
		var apiErr OpenAIResponse
		if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error != nil {
			return nil, 0, apiErr.Error
		}
		return nil, 0, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var apiResp OpenAIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, 0, fmt.Errorf("failed to parse response: %w", err)
	}

	// Check API error
	if apiResp.Error != nil {
		return nil, 0, apiResp.Error
	}

	// Check if we have choices
	if len(apiResp.Choices) == 0 {
		return nil, 0, fmt.Errorf("API returned no choices")
	}

	// Extract content from response
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
	now := time.Now()
	item := model.MemoryItem{
		ID:            model.GenerateID(),
		Namespace:     fmt.Sprintf("%s/auto/%s", mem.Namespace, time.Now().Format("20060102")),
		NamespaceType: mem.Namespace,
		Title:         mem.Title,
		Content:       mem.Content,
		Summary:       mem.Summary,
		TagsJSON:      toJSON(mem.Tags),
		SourceType:    model.SourceTypeAgent,
		Importance:    mem.Importance,
		Confidence:    mem.Confidence,
		Status:        model.ItemStatusActive,
		CreatedAt:     now,
		UpdatedAt:     now,
		Version:       1,
	}

	// Check for duplicate by content hash (simplified)
	var existing model.MemoryItem
	err := e.db.Where("namespace_type = ? AND content = ?", mem.Namespace, mem.Content).First(&existing).Error
	if err == nil {
		// Duplicate found, skip
		return nil
	}

	return e.db.Create(&item).Error
}

// Helper functions

func calculateHash(text string) string {
	h := sha256.New()
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))[:32] // First 16 bytes
}

func estimateTokens(text string) int {
	// Rough estimate: 1 token ≈ 4 characters
	return len(text) / 4
}

func contains(text string, keywords []string) bool {
	for _, kw := range keywords {
		if len(text) > 0 && len(kw) > 0 {
			// Simple substring match
			for i := 0; i <= len(text)-len(kw); i++ {
				if text[i:i+len(kw)] == kw {
					return true
				}
			}
		}
	}
	return false
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

func valueOrZeroF(ptr *float64) float64 {
	if ptr == nil {
		return 0
	}
	return *ptr
}
