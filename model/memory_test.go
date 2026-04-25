// Package model provides tests for data models.
package model

import (
	"sync"
	"testing"
)

func TestGenerateID(t *testing.T) {
	t.Run("generates valid ULID", func(t *testing.T) {
		id := GenerateID()
		if id == "" {
			t.Fatal("Expected non-empty ID")
		}
		if len(id) != 26 {
			t.Fatalf("Expected ULID length 26, got %d (%s)", len(id), id)
		}
		// ULID should be valid Crockford base32
		for _, c := range id {
			if !isValidCrockfordChar(c) {
				t.Fatalf("Invalid character in ULID: %c", c)
			}
		}
	})

	t.Run("generates unique IDs", func(t *testing.T) {
		id1 := GenerateID()
		id2 := GenerateID()
		if id1 == id2 {
			t.Fatal("Expected different IDs")
		}
	})

	t.Run("concurrent generation", func(t *testing.T) {
		const count = 100
		var wg sync.WaitGroup
		ids := make([]string, count)
		idMap := make(map[string]bool)

		for i := 0; i < count; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				ids[idx] = GenerateID()
			}(i)
		}

		wg.Wait()

		for _, id := range ids {
			if idMap[id] {
				t.Fatalf("Duplicate ID generated: %s", id)
			}
			idMap[id] = true
		}

		if len(idMap) != count {
			t.Fatalf("Expected %d unique IDs, got %d", count, len(idMap))
		}
	})
}

func isValidCrockfordChar(c rune) bool {
	// Crockford base32: 0123456789ABCDEFGHJKMNPQRSTVWXYZ
	valid := "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	for _, v := range valid {
		if c == v {
			return true
		}
	}
	return false
}

func TestMemoryItem_TableName(t *testing.T) {
	item := MemoryItem{}
	if item.TableName() != "memory_items" {
		t.Errorf("Expected table name 'memory_items', got '%s'", item.TableName())
	}
}

func TestMemoryLink_TableName(t *testing.T) {
	link := MemoryLink{}
	if link.TableName() != "memory_links" {
		t.Errorf("Expected table name 'memory_links', got '%s'", link.TableName())
	}
}

func TestNamespaceTypes(t *testing.T) {
	tests := []struct {
		nsType   NamespaceType
		expected string
	}{
		{NamespaceTypeTransient, "transient"},
		{NamespaceTypeProfile, "profile"},
		{NamespaceTypeAction, "action"},
		{NamespaceTypeKnowledge, "knowledge"},
	}

	for _, tt := range tests {
		if string(tt.nsType) != tt.expected {
			t.Errorf("Expected %s, got %s", tt.expected, tt.nsType)
		}
	}
}

func TestItemStatuses(t *testing.T) {
	tests := []struct {
		status   ItemStatus
		expected string
	}{
		{ItemStatusActive, "active"},
		{ItemStatusExpired, "expired"},
		{ItemStatusArchived, "archived"},
		{ItemStatusDeleted, "deleted"},
	}

	for _, tt := range tests {
		if string(tt.status) != tt.expected {
			t.Errorf("Expected %s, got %s", tt.expected, tt.status)
		}
	}
}

func TestTTLPolicies(t *testing.T) {
	tests := []struct {
		policy   TTLPolicy
		expected string
	}{
		{TTLPolicyFixed, "fixed"},
		{TTLPolicySliding, "sliding"},
		{TTLPolicyManual, "manual"},
	}

	for _, tt := range tests {
		if string(tt.policy) != tt.expected {
			t.Errorf("Expected %s, got %s", tt.expected, tt.policy)
		}
	}
}
