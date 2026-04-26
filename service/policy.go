// Package service provides namespace policy management.
package service

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
)

// defaultNamespacePolicies provides default policies for each namespace type.
var defaultNamespacePolicies = map[model.NamespaceType]model.NamespacePolicy{
	model.NamespaceTypeTransient: {
		Namespace:                 "transient/*",
		TTLSeconds:                intPtr(259200), // 3 days
		TTLPolicy:                 model.TTLPolicySliding,
		SlidingTTLThreshold:       3,
		SummaryEnabled:            true,
		SummaryItemTokenThreshold: 500,
		RankWeightsJSON:           `{"fts":0.55,"recency":0.20,"importance":0.15,"confidence":0.10}`,
		DefaultTopK:               10,
	},
	model.NamespaceTypeProfile: {
		Namespace:                 "profile/*",
		TTLSeconds:                nil, // No expiry
		TTLPolicy:                 model.TTLPolicyManual,
		SlidingTTLThreshold:       0,
		SummaryEnabled:            true,
		SummaryItemTokenThreshold: 500,
		RankWeightsJSON:           `{"fts":0.55,"recency":0.15,"importance":0.20,"confidence":0.10}`,
		DefaultTopK:               10,
	},
	model.NamespaceTypeAction: {
		Namespace:                 "action/*",
		TTLSeconds:                intPtr(7776000), // 90 days
		TTLPolicy:                 model.TTLPolicyFixed,
		SlidingTTLThreshold:       0,
		SummaryEnabled:            true,
		SummaryItemTokenThreshold: 500,
		RankWeightsJSON:           `{"fts":0.50,"recency":0.25,"importance":0.15,"confidence":0.10}`,
		DefaultTopK:               10,
	},
	model.NamespaceTypeKnowledge: {
		Namespace:                 "knowledge/*",
		TTLSeconds:                nil, // No expiry
		TTLPolicy:                 model.TTLPolicyManual,
		SlidingTTLThreshold:       0,
		SummaryEnabled:            true,
		SummaryItemTokenThreshold: 1000,
		RankWeightsJSON:           `{"fts":0.60,"recency":0.10,"importance":0.20,"confidence":0.10}`,
		DefaultTopK:               10,
	},
}

// PolicyManager handles namespace policy resolution.
type PolicyManager struct {
	db *gorm.DB
}

// NewPolicyManager creates a new policy manager.
func NewPolicyManager(db *gorm.DB) *PolicyManager {
	return &PolicyManager{db: db}
}

// GetPolicy retrieves the policy for a namespace (exact match first, then type default).
func (pm *PolicyManager) GetPolicy(ctx context.Context, namespace string) (model.NamespacePolicy, error) {
	// Try exact match first
	var policy model.NamespacePolicy
	err := pm.db.WithContext(ctx).Where("namespace = ?", namespace).First(&policy).Error
	if err == nil {
		return policy, nil
	}

	if err != gorm.ErrRecordNotFound {
		return model.NamespacePolicy{}, wrapErr(CodeInternal, "query policy failed", err)
	}

	// Fall back to type default
	nsType := inferNamespaceType(namespace)
	if defaultPolicy, ok := defaultNamespacePolicies[nsType]; ok {
		return defaultPolicy, nil
	}

	// Ultimate fallback
	return defaultNamespacePolicies[model.NamespaceTypeTransient], nil
}

// SetPolicy sets a custom policy for a namespace (exact match only).
func (pm *PolicyManager) SetPolicy(ctx context.Context, policy model.NamespacePolicy) error {
	now := time.Now()
	policy.CreatedAt = now
	policy.UpdatedAt = now

	var existing model.NamespacePolicy
	err := pm.db.WithContext(ctx).Where("namespace = ?", policy.Namespace).First(&existing).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			// Create new
			if err := pm.db.WithContext(ctx).Create(&policy).Error; err != nil {
				return wrapErr(CodeInternal, "create policy failed", err)
			}
			return nil
		}
		return wrapErr(CodeInternal, "query policy failed", err)
	}

	// Update existing
	policy.CreatedAt = existing.CreatedAt
	if err := pm.db.WithContext(ctx).Save(&policy).Error; err != nil {
		return wrapErr(CodeInternal, "update policy failed", err)
	}

	return nil
}

// GetRankWeights parses the rank weights JSON from a policy.
func (pm *PolicyManager) GetRankWeights(policy model.NamespacePolicy) (fts, recency, importance, confidence float64) {
	var weights struct {
		FTS         float64 `json:"fts"`
		Recency     float64 `json:"recency"`
		Importance  float64 `json:"importance"`
		Confidence  float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(policy.RankWeightsJSON), &weights); err != nil {
		// Return defaults
		return 0.55, 0.20, 0.15, 0.10
	}
	return weights.FTS, weights.Recency, weights.Importance, weights.Confidence
}

// inferNamespaceType extracts the namespace type from the namespace string.
func inferNamespaceType(namespace string) model.NamespaceType {
	// Simple prefix matching
	switch {
	case len(namespace) >= 9 && namespace[:9] == "transient":
		return model.NamespaceTypeTransient
	case len(namespace) >= 7 && namespace[:7] == "profile":
		return model.NamespaceTypeProfile
	case len(namespace) >= 6 && namespace[:6] == "action":
		return model.NamespaceTypeAction
	case len(namespace) >= 9 && namespace[:9] == "knowledge":
		return model.NamespaceTypeKnowledge
	default:
		return model.NamespaceTypeTransient
	}
}

func intPtr(i int) *int {
	return &i
}
