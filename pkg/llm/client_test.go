// Package llm provides tests for LLM client abstractions.
package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/lengzhao/memory/model"
	memerrors "github.com/lengzhao/memory/pkg/errors"
)

// mockClient is a test double for the Client interface.
type mockClient struct {
	response string
	tokens   int
	err      error
}

func (m *mockClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	if m.err != nil {
		return CompletionResponse{}, m.err
	}
	return CompletionResponse{
		Content:     m.response,
		TotalTokens: m.tokens,
	}, nil
}

func TestExtractor_Extract(t *testing.T) {
	t.Run("successful extraction", func(t *testing.T) {
		response := `{"memories": [{"namespace": "profile", "title": "Test", "content": "Test content", "importance": 80, "confidence": 0.9}]}`
		client := &mockClient{response: response, tokens: 100}
		extractor := NewExtractor(client)

		result, err := extractor.Extract(context.Background(), "Some dialog text", 0.7)
		if err != nil {
			t.Fatalf("Extract failed: %v", err)
		}

		if len(result.Memories) != 1 {
			t.Fatalf("Expected 1 memory, got %d", len(result.Memories))
		}

		mem := result.Memories[0]
		if mem.GetNamespaceType() != model.NamespaceTypeProfile {
			t.Errorf("Expected namespace profile, got %s (%s)", mem.Namespace, mem.GetNamespaceType())
		}
		if mem.Title != "Test" {
			t.Errorf("Expected title 'Test', got %s", mem.Title)
		}
		if result.TotalTokens != 100 {
			t.Errorf("Expected 100 tokens, got %d", result.TotalTokens)
		}
	})

	t.Run("direct array response", func(t *testing.T) {
		response := `[{"namespace": "knowledge", "title": "Array Test", "content": "Content", "importance": 50, "confidence": 0.8}]`
		client := &mockClient{response: response, tokens: 50}
		extractor := NewExtractor(client)

		result, err := extractor.Extract(context.Background(), "Dialog", 0.7)
		if err != nil {
			t.Fatalf("Extract failed: %v", err)
		}

		if len(result.Memories) != 1 {
			t.Fatalf("Expected 1 memory from array, got %d", len(result.Memories))
		}
	})

	t.Run("single object response", func(t *testing.T) {
		response := `{"namespace": "action", "title": "Single", "content": "Content", "importance": 60, "confidence": 0.85}`
		client := &mockClient{response: response, tokens: 30}
		extractor := NewExtractor(client)

		result, err := extractor.Extract(context.Background(), "Dialog", 0.8)
		if err != nil {
			t.Fatalf("Extract failed: %v", err)
		}

		// With confidence 0.85 and minConfidence 0.8, it should be included
		if len(result.Memories) != 1 {
			t.Fatalf("Expected 1 memory from single object, got %d", len(result.Memories))
		}
	})

	t.Run("confidence filtering", func(t *testing.T) {
		response := `{"memories": [
			{"namespace": "profile", "title": "High", "content": "Content", "importance": 50, "confidence": 0.9},
			{"namespace": "profile", "title": "Low", "content": "Content", "importance": 50, "confidence": 0.5}
		]}`
		client := &mockClient{response: response, tokens: 100}
		extractor := NewExtractor(client)

		result, err := extractor.Extract(context.Background(), "Dialog", 0.7)
		if err != nil {
			t.Fatalf("Extract failed: %v", err)
		}

		if len(result.Memories) != 1 {
			t.Fatalf("Expected 1 memory (filtered), got %d", len(result.Memories))
		}

		if result.Memories[0].Title != "High" {
			t.Error("Expected high confidence memory to remain")
		}
	})

	t.Run("llm error", func(t *testing.T) {
		client := &mockClient{err: errors.New("api error")}
		extractor := NewExtractor(client)

		_, err := extractor.Extract(context.Background(), "Dialog", 0.7)
		if err == nil {
			t.Fatal("Expected error from LLM")
		}

		// Check it's wrapped as LLM error
		memErr, ok := memerrors.As(err)
		if !ok {
			t.Fatal("Expected MemoryError")
		}
		if memErr.Code != memerrors.CodeLLM {
			t.Errorf("Expected LLM error code, got %s", memErr.Code)
		}
	})

	t.Run("invalid json response", func(t *testing.T) {
		client := &mockClient{response: "invalid json"}
		extractor := NewExtractor(client)

		_, err := extractor.Extract(context.Background(), "Dialog", 0.7)
		if err == nil {
			t.Fatal("Expected error for invalid JSON")
		}
	})
}

func TestExtractMemory_JSON(t *testing.T) {
	mem := ExtractMemory{
		Namespace:  "knowledge",
		Title:      "Test",
		Content:    "Content",
		Tags:       []string{"tag1", "tag2"},
		Importance: 75,
		Confidence: 0.85,
		Reasoning:  "Because...",
	}

	// Basic validation
	if mem.Namespace != "knowledge" {
		t.Error("Namespace mismatch")
	}
	if mem.GetNamespaceType() != model.NamespaceTypeKnowledge {
		t.Errorf("Expected namespace type knowledge, got %s", mem.GetNamespaceType())
	}
	if len(mem.Tags) != 2 {
		t.Errorf("Expected 2 tags, got %d", len(mem.Tags))
	}
}
