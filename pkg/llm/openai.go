// Package llm provides LLM client implementations.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/lengzhao/memory/pkg/errors"
)

// OpenAIClient implements the Client interface for OpenAI API.
type OpenAIClient struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	model      string
	defaultMaxTokens   int
	defaultTemperature float64
}

// OpenAIOption configures the OpenAI client.
type OpenAIOption func(*OpenAIClient)

// WithBaseURL sets a custom base URL (for Azure or proxies).
func WithBaseURL(url string) OpenAIOption {
	return func(c *OpenAIClient) {
		c.baseURL = url
	}
}

// WithModel sets the default model.
func WithModel(model string) OpenAIOption {
	return func(c *OpenAIClient) {
		c.model = model
	}
}

// WithTimeout sets the HTTP client timeout.
func WithTimeout(timeout time.Duration) OpenAIOption {
	return func(c *OpenAIClient) {
		c.httpClient.Timeout = timeout
	}
}

// NewOpenAIClient creates a new OpenAI client.
func NewOpenAIClient(apiKey string, opts ...OpenAIOption) *OpenAIClient {
	c := &OpenAIClient{
		apiKey:             apiKey,
		baseURL:            "https://api.openai.com/v1",
		httpClient:         &http.Client{Timeout: 30 * time.Second},
		model:              "gpt-4o",
		defaultMaxTokens:   4096,
		defaultTemperature: 0.3,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// openAIRequest represents the OpenAI chat completion request.
type openAIRequest struct {
	Model          string          `json:"model"`
	Messages       []openAIMessage `json:"messages"`
	Temperature    float64         `json:"temperature"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"` // "json_object" for JSON mode
}

// openAIResponse represents the OpenAI chat completion response.
type openAIResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
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

func (e *openAIError) Error() string {
	return fmt.Sprintf("openai error: %s (type: %s, code: %s)", e.Message, e.Type, e.Code)
}

// Complete implements the Client interface.
func (c *OpenAIClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = c.defaultMaxTokens
	}
	if req.Temperature == 0 {
		req.Temperature = c.defaultTemperature
	}

	body := openAIRequest{
		Model:       req.Model,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Messages: []openAIMessage{
			{Role: "system", Content: req.SystemPrompt},
			{Role: "user", Content: req.UserPrompt},
		},
	}
	if req.JSONMode {
		body.ResponseFormat = &responseFormat{Type: "json_object"}
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return CompletionResponse{}, errors.Wrap(errors.CodeInternal, "failed to marshal request", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return CompletionResponse{}, errors.Wrap(errors.CodeInternal, "failed to create request", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, errors.Wrap(errors.CodeLLM, "request failed", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return CompletionResponse{}, errors.Wrap(errors.CodeInternal, "failed to read response", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr openAIResponse
		if err := json.Unmarshal(respBody, &apiErr); err == nil && apiErr.Error != nil {
			return CompletionResponse{}, errors.Wrap(errors.CodeLLM, apiErr.Error.Error(), nil)
		}
		return CompletionResponse{}, errors.New(errors.CodeLLM, fmt.Sprintf("http %d: %s", resp.StatusCode, string(respBody)))
	}

	var apiResp openAIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return CompletionResponse{}, errors.Wrap(errors.CodeInternal, "failed to parse response", err)
	}

	if apiResp.Error != nil {
		return CompletionResponse{}, errors.Wrap(errors.CodeLLM, apiResp.Error.Error(), nil)
	}

	if len(apiResp.Choices) == 0 {
		return CompletionResponse{}, errors.New(errors.CodeLLM, "no choices in response")
	}

	return CompletionResponse{
		Content:     apiResp.Choices[0].Message.Content,
		TotalTokens: apiResp.Usage.TotalTokens,
	}, nil
}
