package service

import (
	"context"
	"testing"

	"github.com/lengzhao/memory/model"
	"github.com/lengzhao/memory/store"
)

func isolatedCtx(sessionID string) context.Context {
	return WithIsolation(context.Background(), "t1", "u1", sessionID, "planner")
}

func TestMemoryService_IsolationRememberWithContext(t *testing.T) {
	db := store.SetupTestDB(t)
	svc := NewMemoryService(db)

	_, err := svc.Remember(isolatedCtx("s1"), RememberRequest{
		NamespaceType: model.NamespaceTypeTransient,
		Content:       "x",
	})
	if err != nil {
		t.Fatalf("expected remember success in isolation mode, got %v", err)
	}
}

func TestMemoryService_IsolationRecallScopedBySession(t *testing.T) {
	db := store.SetupTestDB(t)
	svc := NewMemoryService(db)

	_, err := svc.Remember(isolatedCtx("s1"), RememberRequest{
		NamespaceType: model.NamespaceTypeTransient,
		Content:       "from-session-1",
	})
	if err != nil {
		t.Fatalf("remember s1 failed: %v", err)
	}

	_, err = svc.Remember(isolatedCtx("s2"), RememberRequest{
		NamespaceType: model.NamespaceTypeTransient,
		Content:       "from-session-2",
	})
	if err != nil {
		t.Fatalf("remember s2 failed: %v", err)
	}

	hits1, err := svc.Recall(isolatedCtx("s1"), RecallRequest{TopK: 10})
	if err != nil {
		t.Fatalf("recall s1 failed: %v", err)
	}
	if len(hits1) != 1 || hits1[0].Content != "from-session-1" {
		t.Fatalf("expected only s1 data, got %+v", hits1)
	}

	hits2, err := svc.Recall(isolatedCtx("s2"), RecallRequest{TopK: 10})
	if err != nil {
		t.Fatalf("recall s2 failed: %v", err)
	}
	if len(hits2) != 1 || hits2[0].Content != "from-session-2" {
		t.Fatalf("expected only s2 data, got %+v", hits2)
	}
}

func TestMemoryService_IsolationRecallWithContext(t *testing.T) {
	db := store.SetupTestDB(t)
	svc := NewMemoryService(db)

	_, err := svc.Recall(isolatedCtx("s1"), RecallRequest{
	})
	if err != nil {
		t.Fatalf("expected recall success in isolation mode, got %v", err)
	}
}

func TestWithIsolation_EmptyValuesFallbackToDefault(t *testing.T) {
	ctx := WithIsolation(context.Background(), "", "u1", "", "planner")

	meta, err := IsolationFromContext(ctx)
	if err != nil {
		t.Fatalf("expected isolation context to be valid with defaults, got %v", err)
	}
	if meta.TenantID != "default" {
		t.Fatalf("expected tenant default, got %q", meta.TenantID)
	}
	if meta.SessionID != "default" {
		t.Fatalf("expected session default, got %q", meta.SessionID)
	}
	if meta.UserID != "u1" {
		t.Fatalf("expected user u1, got %q", meta.UserID)
	}
	if meta.AgentID != "planner" {
		t.Fatalf("expected agent planner, got %q", meta.AgentID)
	}
}

