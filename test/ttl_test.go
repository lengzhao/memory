package test

import (
	"testing"
	"time"

	"github.com/lengzhao/memory"
	"github.com/lengzhao/memory/model"
)

// TestTTL_FixedExpiration tests fixed TTL expiration
func TestTTL_FixedExpiration(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create item with 1 second TTL
	ttl := 1
	id, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace:     "test/ttl-fixed",
		NamespaceType: memory.NamespaceTransient,
		Content:       "Short lived content",
		TTLSeconds:    &ttl,
	})
	if err != nil {
		t.Fatalf("Failed to remember: %v", err)
	}

	// Should be found immediately
	hits, err := svc.Recall(ctx, memory.RecallRequest{
		Namespaces: []string{"test/ttl-fixed"},
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("Expected 1 hit before expiry, got %d", len(hits))
	}

	// Wait for expiry
	time.Sleep(2 * time.Second)

	// Should not be found (expired excluded by default)
	hits, err = svc.Recall(ctx, memory.RecallRequest{
		Namespaces: []string{"test/ttl-fixed"},
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("Expected 0 hits after expiry, got %d", len(hits))
	}

	// Verify item still exists but is expired
	var item model.MemoryItem
	tdb.DB.WithContext(ctx).First(&item, "id = ?", id)
	if item.ExpiresAt == nil || time.Now().Before(*item.ExpiresAt) {
		t.Error("Item should be expired")
	}
}

// TestTTL_IncludeExpired tests including expired items in recall
func TestTTL_IncludeExpired(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create item with 1 second TTL
	ttl := 1
	_, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace:  "test/ttl-include",
		Content:    "Content",
		TTLSeconds: &ttl,
	})
	if err != nil {
		t.Fatalf("Failed to remember: %v", err)
	}

	// Wait for expiry
	time.Sleep(2 * time.Second)

	// Should not be found without IncludeExpired
	hits, _ := svc.Recall(ctx, memory.RecallRequest{
		Namespaces: []string{"test/ttl-include"},
	})
	if len(hits) != 0 {
		t.Error("Should not find expired item without IncludeExpired")
	}

	// Should be found with IncludeExpired
	hits, err = svc.Recall(ctx, memory.RecallRequest{
		Namespaces:     []string{"test/ttl-include"},
		IncludeExpired: true,
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("Expected 1 hit with IncludeExpired=true, got %d", len(hits))
	}
}

// TestTTL_CleanupExpired tests cleanup of expired items
func TestTTL_CleanupExpired(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create item with very short TTL and wait for it to expire
	ttl := 1 // 1 second
	id, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace:  "test/ttl-cleanup",
		Content:    "Expired content",
		TTLSeconds: &ttl,
	})
	if err != nil {
		t.Fatalf("Failed to remember: %v", err)
	}

	// Wait for expiry
	time.Sleep(2 * time.Second)

	// Run cleanup
	count, err := svc.CleanupExpired(ctx)
	if err != nil {
		t.Fatalf("Failed to cleanup: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 item cleaned up, got %d", count)
	}

	// Verify status changed to expired
	var item model.MemoryItem
	tdb.DB.WithContext(ctx).First(&item, "id = ?", id)
	if item.Status != model.ItemStatusExpired {
		t.Errorf("Expected status expired, got %v", item.Status)
	}
}

// TestTTL_RenewExpiration tests manual expiration renewal
func TestTTL_RenewExpiration(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create item with short TTL
	ttl := 1
	id, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace:  "test/ttl-renew",
		Content:    "Content to renew",
		TTLSeconds: &ttl,
	})
	if err != nil {
		t.Fatalf("Failed to remember: %v", err)
	}

	// Get original expiry
	var originalItem model.MemoryItem
	tdb.DB.WithContext(ctx).First(&originalItem, "id = ?", id)
	originalExpiry := *originalItem.ExpiresAt

	// Renew for another hour
	newTTL := 3600
	err = svc.RenewExpiration(ctx, id, newTTL)
	if err != nil {
		t.Fatalf("Failed to renew: %v", err)
	}

	// Verify new expiry is later
	var renewedItem model.MemoryItem
	tdb.DB.WithContext(ctx).First(&renewedItem, "id = ?", id)
	if renewedItem.ExpiresAt == nil {
		t.Fatal("Expected renewed expiry to be set")
	}

	if !renewedItem.ExpiresAt.After(originalExpiry) {
		t.Errorf("Expected renewed expiry %v to be after original %v", *renewedItem.ExpiresAt, originalExpiry)
	}
}

// TestTTL_SlidingRenewal tests sliding TTL renewal on touch
func TestTTL_SlidingRenewal(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create item with TTL
	ttl := 60
	id, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace:  "test/ttl-sliding",
		Content:    "Sliding TTL content",
		TTLSeconds: &ttl,
	})
	if err != nil {
		t.Fatalf("Failed to remember: %v", err)
	}

	// Touch with renewal every 3rd access
	renewed, err := svc.TouchWithRenew(ctx, id, 3, 120)
	if err != nil {
		t.Fatalf("First touch failed: %v", err)
	}
	if renewed {
		t.Error("Should not renew on first touch")
	}

	renewed, err = svc.TouchWithRenew(ctx, id, 3, 120)
	if err != nil {
		t.Fatalf("Second touch failed: %v", err)
	}
	if renewed {
		t.Error("Should not renew on second touch")
	}

	// Third touch should renew
	renewed, err = svc.TouchWithRenew(ctx, id, 3, 120)
	if err != nil {
		t.Fatalf("Third touch failed: %v", err)
	}
	if !renewed {
		t.Error("Should renew on third touch")
	}
}
