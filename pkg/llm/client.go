// Package llm provides LLM client abstractions for memory extraction.
package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lengzhao/memory/model"
	"github.com/lengzhao/memory/pkg/errors"
)

// Client is the interface for LLM providers.
type Client interface {
	// Complete sends a completion request to the LLM.
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}

// CompletionRequest represents a request to complete/generate text.
type CompletionRequest struct {
	Model       string
	SystemPrompt string
	UserPrompt  string
	Temperature float64
	MaxTokens   int
	JSONMode    bool // If true, request JSON output
}

// CompletionResponse represents the response from an LLM.
type CompletionResponse struct {
	Content     string
	TotalTokens int
	// Usage can be extended with prompt/completion tokens
}

// ExtractMemory represents a single extracted memory item.
type ExtractMemory struct {
	Namespace    string                 `json:"namespace"` // Raw string from LLM
	Title        string                 `json:"title"`
	Content      string                 `json:"content"`
	Summary      string                 `json:"summary,omitempty"`
	Tags         []string               `json:"tags,omitempty"`
	Importance   int                    `json:"importance"`
	Confidence   float64                `json:"confidence"`
	Reasoning    string                 `json:"reasoning,omitempty"`
	TaskMetadata map[string]interface{} `json:"task_metadata,omitempty"`
}

// GetNamespaceType returns the namespace type from the raw string.
func (m ExtractMemory) GetNamespaceType() model.NamespaceType {
	switch m.Namespace {
	case "transient":
		return model.NamespaceTypeTransient
	case "profile":
		return model.NamespaceTypeProfile
	case "action":
		return model.NamespaceTypeAction
	case "knowledge":
		return model.NamespaceTypeKnowledge
	default:
		return model.NamespaceTypeTransient
	}
}

// ExtractResult represents the result of memory extraction.
type ExtractResult struct {
	Memories    []ExtractMemory
	TotalTokens int
}

// Extractor handles memory extraction using an LLM client.
type Extractor struct {
	client Client
}

// NewExtractor creates a new extractor with the given client.
func NewExtractor(client Client) *Extractor {
	return &Extractor{client: client}
}

// Extract extracts memories from dialog text using the configured LLM.
func (e *Extractor) Extract(ctx context.Context, dialog string, minConfidence float64) (*ExtractResult, error) {
	if minConfidence == 0 {
		minConfidence = 0.7
	}

	// Build the extraction prompt
	sysPrompt := `You are a memory extraction assistant. Analyze the following dialog and extract valuable memories.

CLASSIFICATION RULES (4 categories):
- "transient": Temporary conversation context, short-lived facts that become irrelevant after the session
- "profile": User preferences, personal information, habits, likes/dislikes - long-term stable traits  
- "action": Action items, todos, tasks, goals with deadlines or priorities - things that need to be done
- "knowledge": Learned facts, concepts, skills, methods, procedures - information that was learned

OUTPUT FORMAT:
Return a JSON object with a "memories" key containing an array of memory objects:
{
  "memories": [
    {
      "namespace": "transient|profile|action|knowledge",
      "title": "Short descriptive title (max 10 words)",
      "content": "Full detailed content",
      "summary": "One sentence summary",
      "tags": ["relevant", "keywords"],
      "importance": 50,
      "confidence": 0.85,
      "reasoning": "Why this classification was chosen",
      "task_metadata": {"deadline": "2024-01-01", "priority": "high|medium|low"}
    }
  ]
}

GUIDELINES:
- Only extract high-confidence information (confidence >= 0.7)
- Use specific, descriptive tags
- Importance: 0-100 scale, higher for critical information
- Confidence: 0.0-1.0 based on clarity in source text
- task_metadata only required for "action" namespace`

	userPrompt := fmt.Sprintf(`Analyze the following dialog and extract valuable memories.

Dialog:
"""
%s
"""

Extract memories and return them as: {"memories": [...]}`, dialog)

	req := CompletionRequest{
		SystemPrompt: sysPrompt,
		UserPrompt:   userPrompt,
		Temperature:  0.3,
		MaxTokens:    4096,
		JSONMode:     true,
	}

	resp, err := e.client.Complete(ctx, req)
	if err != nil {
		return nil, errors.Wrap(errors.CodeLLM, "llm completion failed", err)
	}

	// Parse the response
	var result struct {
		Memories []ExtractMemory `json:"memories"`
	}
	parseErr := json.Unmarshal([]byte(resp.Content), &result)
	if parseErr != nil || len(result.Memories) == 0 {
		// Try parsing as direct array
		var memories []ExtractMemory
		if err := json.Unmarshal([]byte(resp.Content), &memories); err == nil && len(memories) > 0 {
			result.Memories = memories
		} else {
			// Try parsing single memory
			var single ExtractMemory
			if err := json.Unmarshal([]byte(resp.Content), &single); err == nil {
				result.Memories = []ExtractMemory{single}
			} else if parseErr != nil {
				return nil, errors.Wrap(errors.CodeLLM, "failed to parse llm response", parseErr)
			} else {
				return nil, errors.New(errors.CodeLLM, "could not parse llm response as memories")
			}
		}
	}

	// Filter by confidence
	var filtered []ExtractMemory
	for _, m := range result.Memories {
		if m.Confidence >= minConfidence {
			filtered = append(filtered, m)
		}
	}

	return &ExtractResult{
		Memories:    filtered,
		TotalTokens: resp.TotalTokens,
	}, nil
}
