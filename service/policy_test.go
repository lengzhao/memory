// Package service provides tests for PolicyManager.
package service

import (
	"context"
	"testing"

	"github.com/lengzhao/memory/model"
	"github.com/lengzhao/memory/store"
)

func TestPolicyManager_GetPolicy(t *testing.T) {
	db := store.SetupTestDB(t)
	pm := NewPolicyManager(db)
	ctx := context.Background()

	t.Run("default policy for transient", func(t *testing.T) {
		policy, err := pm.GetPolicy(ctx, "transient/session123")
		if err != nil {
			t.Fatalf("GetPolicy failed: %v", err)
		}
		if policy.Namespace != "transient/*" {
			t.Fatalf("Expected namespace 'transient/*', got '%s'", policy.Namespace)
		}
		if policy.TTLPolicy != model.TTLPolicySliding {
			t.Fatalf("Expected sliding TTL policy, got %s", policy.TTLPolicy)
		}
	})

	t.Run("default policy for profile", func(t *testing.T) {
		policy, err := pm.GetPolicy(ctx, "profile/user123")
		if err != nil {
			t.Fatalf("GetPolicy failed: %v", err)
		}
		if policy.TTLPolicy != model.TTLPolicyManual {
			t.Fatalf("Expected manual TTL policy, got %s", policy.TTLPolicy)
		}
		if policy.TTLSeconds != nil {
			t.Fatal("Expected nil TTL for profile")
		}
	})

	t.Run("default policy for action", func(t *testing.T) {
		policy, err := pm.GetPolicy(ctx, "action/task123")
		if err != nil {
			t.Fatalf("GetPolicy failed: %v", err)
		}
		if policy.TTLPolicy != model.TTLPolicyFixed {
			t.Fatalf("Expected fixed TTL policy, got %s", policy.TTLPolicy)
		}
	})

	t.Run("custom policy overrides default", func(t *testing.T) {
		custom := model.NamespacePolicy{
			Namespace:  "test/custom",
			TTLSeconds: intPtr(3600),
			TTLPolicy:  model.TTLPolicyFixed,
		}
		if err := pm.SetPolicy(ctx, custom); err != nil {
			t.Fatalf("SetPolicy failed: %v", err)
		}

		policy, err := pm.GetPolicy(ctx, "test/custom")
		if err != nil {
			t.Fatalf("GetPolicy failed: %v", err)
		}
		if policy.Namespace != "test/custom" {
			t.Fatalf("Expected custom namespace, got %s", policy.Namespace)
		}
		if *policy.TTLSeconds != 3600 {
			t.Fatalf("Expected TTL 3600, got %d", *policy.TTLSeconds)
		}
	})
}

func TestPolicyManager_SetPolicy(t *testing.T) {
	db := store.SetupTestDB(t)
	pm := NewPolicyManager(db)
	ctx := context.Background()

	t.Run("create new policy", func(t *testing.T) {
		policy := model.NamespacePolicy{
			Namespace:  "test/new",
			TTLSeconds: intPtr(7200),
			TTLPolicy:  model.TTLPolicySliding,
		}

		err := pm.SetPolicy(ctx, policy)
		if err != nil {
			t.Fatalf("SetPolicy failed: %v", err)
		}

		// Verify
		retrieved, err := pm.GetPolicy(ctx, "test/new")
		if err != nil {
			t.Fatalf("GetPolicy failed: %v", err)
		}
		if *retrieved.TTLSeconds != 7200 {
			t.Fatalf("Expected TTL 7200, got %d", *retrieved.TTLSeconds)
		}
	})

	t.Run("update existing policy", func(t *testing.T) {
		// First create
		policy := model.NamespacePolicy{
			Namespace:  "test/update",
			TTLSeconds: intPtr(3600),
		}
		pm.SetPolicy(ctx, policy)

		// Update
		policy.TTLSeconds = intPtr(7200)
		err := pm.SetPolicy(ctx, policy)
		if err != nil {
			t.Fatalf("SetPolicy update failed: %v", err)
		}

		// Verify
		retrieved, _ := pm.GetPolicy(ctx, "test/update")
		if *retrieved.TTLSeconds != 7200 {
			t.Fatalf("Expected updated TTL 7200, got %d", *retrieved.TTLSeconds)
		}
	})
}

func TestPolicyManager_GetRankWeights(t *testing.T) {
	pm := NewPolicyManager(nil) // DB not needed for this test

	t.Run("parse valid weights", func(t *testing.T) {
		policy := model.NamespacePolicy{
			RankWeightsJSON: `{"fts":0.6,"recency":0.2,"importance":0.15,"confidence":0.05}`,
		}
		fts, recency, importance, confidence := pm.GetRankWeights(policy)

		if fts != 0.6 {
			t.Fatalf("Expected fts 0.6, got %f", fts)
		}
		if recency != 0.2 {
			t.Fatalf("Expected recency 0.2, got %f", recency)
		}
		if importance != 0.15 {
			t.Fatalf("Expected importance 0.15, got %f", importance)
		}
		if confidence != 0.05 {
			t.Fatalf("Expected confidence 0.05, got %f", confidence)
		}
	})

	t.Run("invalid json returns defaults", func(t *testing.T) {
		policy := model.NamespacePolicy{
			RankWeightsJSON: `invalid json`,
		}
		fts, recency, importance, confidence := pm.GetRankWeights(policy)

		if fts != 0.55 || recency != 0.20 || importance != 0.15 || confidence != 0.10 {
			t.Fatal("Expected default weights for invalid JSON")
		}
	})
}

func TestInferNamespaceType(t *testing.T) {
	tests := []struct {
		namespace string
		expected  model.NamespaceType
	}{
		{"transient/session1", model.NamespaceTypeTransient},
		{"profile/user1", model.NamespaceTypeProfile},
		{"action/task1", model.NamespaceTypeAction},
		{"knowledge/topic1", model.NamespaceTypeKnowledge},
		{"unknown/thing", model.NamespaceTypeTransient}, // default
	}

	for _, tt := range tests {
		result := inferNamespaceType(tt.namespace)
		if result != tt.expected {
			t.Errorf("inferNamespaceType(%s) = %s, want %s", tt.namespace, result, tt.expected)
		}
	}
}
