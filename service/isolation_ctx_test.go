package service

import (
	"context"
	"errors"
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

	_, err := svc.Recall(isolatedCtx("s1"), RecallRequest{})
	if err != nil {
		t.Fatalf("expected recall success in isolation mode, got %v", err)
	}
}

func TestWithIsolation_BlankValuesFailClosed(t *testing.T) {
	// Blank identifiers are stored as-is (no silent "default"), and operations that run with a
	// partial identity are rejected instead of touching the shared namespaces.
	ctx := WithIsolation(context.Background(), "", "u1", "", "planner")

	meta := IsolationFromContext(ctx)
	if meta.TenantID != "" || meta.SessionID != "" {
		t.Fatalf("blank identifiers must not be defaulted, got %+v", meta)
	}
	if meta.UserID != "u1" || meta.AgentID != "planner" {
		t.Fatalf("expected user/agent to be preserved, got %+v", meta)
	}

	svc := NewMemoryService(store.SetupTestDB(t))
	_, err := svc.Remember(ctx, RememberRequest{NamespaceType: model.NamespaceTypeTransient, Content: "x"})
	if err == nil {
		t.Fatal("expected an incomplete isolation context to be rejected")
	}
	var me *MemoryError
	if !errors.As(err, &me) || me.Code != CodeValidation {
		t.Fatalf("expected VALIDATION error, got %v", err)
	}
}

func TestAllowGlobal_MissingIsolationFailsClosed(t *testing.T) {
	svc := NewMemoryService(store.SetupTestDB(t))
	_, err := svc.Remember(context.Background(), RememberRequest{NamespaceType: model.NamespaceTypeTransient, Content: "x"})
	if err == nil {
		t.Fatal("expected a missing isolation context to be rejected")
	}
	var me *MemoryError
	if !errors.As(err, &me) || me.Code != CodeUnauthorized {
		t.Fatalf("expected UNAUTHORIZED error, got %v", err)
	}

	// AllowGlobal opts a single-user deployment into the global namespaces.
	if _, err := svc.Remember(AllowGlobal(context.Background()), RememberRequest{NamespaceType: model.NamespaceTypeTransient, Content: "x"}); err != nil {
		t.Fatalf("expected global mode to work, got %v", err)
	}
}
