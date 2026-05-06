package service

import (
	"context"
	"testing"

	"github.com/lengzhao/memory/model"
)

func TestApplyExtractPolicy_dropTransientEphemeral(t *testing.T) {
	in := []ExtractedMemory{
		{
			Namespace:  model.NamespaceTypeTransient,
			Title:      "当前时间",
			Summary:    "现在是 2026-05-05（UTC）",
			Confidence: 0.95,
		},
		{
			Namespace:  model.NamespaceTypeKnowledge,
			Title:      "会议主题",
			Summary:    "明天 PMO + AI Infra 对齐",
			Confidence: 0.9,
		},
		{
			Namespace:  model.NamespaceTypeTransient,
			Title:      "Note",
			Summary:    "user prefers dark mode",
			Confidence: 0.8,
		},
	}
	out := ApplyExtractPolicy(in, &ExtractPolicy{DropTransientEphemeral: true})
	if len(out) != 2 {
		t.Fatalf("expected 2 kept, got %d: %+v", len(out), out)
	}
}

func TestApplyPostExtractPipeline_minConfidenceAndPolicy(t *testing.T) {
	mem := []ExtractedMemory{
		{Namespace: model.NamespaceTypeKnowledge, Title: "low", Confidence: 0.1},
		{Namespace: model.NamespaceTypeTransient, Title: "当前时间", Summary: "UTC", Confidence: 0.99},
		{Namespace: model.NamespaceTypeKnowledge, Title: "ok", Confidence: 0.9},
	}
	out, err := applyPostExtractPipeline(context.Background(), mem, ExtractRequest{
		MinConfidence: 0.7,
		ExtractPolicy: &ExtractPolicy{DropTransientEphemeral: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Title != "ok" {
		t.Fatalf("unexpected: %+v", out)
	}
}

func TestApplyPostExtractPipeline_postHook(t *testing.T) {
	mem := []ExtractedMemory{
		{Namespace: model.NamespaceTypeKnowledge, Title: "a", Confidence: 0.9},
		{Namespace: model.NamespaceTypeKnowledge, Title: "b", Confidence: 0.9},
	}
	out, err := applyPostExtractPipeline(context.Background(), mem, ExtractRequest{
		MinConfidence: 0.5,
		PostExtractHook: func(_ context.Context, m []ExtractedMemory) ([]ExtractedMemory, error) {
			return m[:1], nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Title != "a" {
		t.Fatalf("unexpected: %+v", out)
	}
}
