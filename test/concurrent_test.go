package test

import (
	"testing"
)

// TestConcurrent_Remember - 原始实现已迁移到 concurrent_sqlite_test.go
// 使用 file::memory:?cache=shared 模式支持并发
func TestConcurrent_Remember(t *testing.T) {
	t.Skip("使用 concurrent_sqlite_test.go 中的 TestConcurrent_Remember_WithSharedCache")
}

// TestConcurrent_UpdateConflict - 原始实现已迁移到 concurrent_sqlite_test.go
func TestConcurrent_UpdateConflict(t *testing.T) {
	t.Skip("使用 concurrent_sqlite_test.go 中的 TestConcurrent_UpdateConflict_WithSharedCache")
}

// TestConcurrent_Touch - 原始实现已迁移到 concurrent_sqlite_test.go
func TestConcurrent_Touch(t *testing.T) {
	t.Skip("使用 concurrent_sqlite_test.go 中的 TestConcurrent_Touch_WithSharedCache")
}

// TestConcurrent_RecallDuringWrite - 原始实现已迁移到 concurrent_sqlite_test.go
func TestConcurrent_RecallDuringWrite(t *testing.T) {
	t.Skip("使用 concurrent_sqlite_test.go 中的 TestConcurrent_RecallDuringWrite_WithSharedCache")
}
