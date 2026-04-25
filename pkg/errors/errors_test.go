// Package errors provides tests for error handling.
package errors

import (
	"errors"
	"testing"
)

func TestNew(t *testing.T) {
	err := New(CodeNotFound, "item not found")

	if err.Code != CodeNotFound {
		t.Errorf("Expected code %s, got %s", CodeNotFound, err.Code)
	}
	if err.Message != "item not found" {
		t.Errorf("Expected message 'item not found', got %s", err.Message)
	}
	if err.Retry {
		t.Error("Expected Retry to be false for NOT_FOUND")
	}
}

func TestWrap(t *testing.T) {
	cause := errors.New("underlying error")
	err := Wrap(CodeInternal, "operation failed", cause)

	if err.Code != CodeInternal {
		t.Errorf("Expected code %s, got %s", CodeInternal, err.Code)
	}
	if err.Cause != cause {
		t.Error("Expected cause to be set")
	}
	if err.Error() != "[INTERNAL] operation failed: underlying error" {
		t.Errorf("Unexpected error message: %s", err.Error())
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		code     ErrorCode
		expected bool
	}{
		{CodeConflict, true},
		{CodeLLM, true},
		{CodeNotFound, false},
		{CodeInternal, false},
		{CodeValidation, false},
	}

	for _, tt := range tests {
		got := IsRetryable(tt.code)
		if got != tt.expected {
			t.Errorf("IsRetryable(%s) = %v, want %v", tt.code, got, tt.expected)
		}
	}
}

func TestAs(t *testing.T) {
	// Test extracting MemoryError
	memErr := New(CodeNotFound, "test")
	extracted, ok := As(memErr)
	if !ok {
		t.Error("Expected to extract MemoryError")
	}
	if extracted.Code != CodeNotFound {
		t.Error("Expected code to match")
	}

	// Test with wrapped error
	wrapped := Wrap(CodeInternal, "wrapped", memErr)
	extracted2, ok2 := As(wrapped)
	if !ok2 {
		t.Error("Expected to extract MemoryError from wrapped")
	}
	if extracted2.Code != CodeInternal {
		t.Error("Expected code INTERNAL from wrapper")
	}

	// Test with regular error
	regular := errors.New("regular error")
	_, ok3 := As(regular)
	if ok3 {
		t.Error("Expected not to extract from regular error")
	}
}

func TestMap(t *testing.T) {
	tests := []struct {
		input    error
		expected ErrorCode
	}{
		{ErrNotFound, CodeNotFound},
		{ErrConflict, CodeConflict},
		{ErrDuplicate, CodeDuplicate},
		{ErrValidation, CodeValidation},
		{ErrUnauthorized, CodeUnauthorized},
		{ErrLLM, CodeLLM},
		{errors.New("unknown"), CodeInternal},
		{nil, ""}, // nil returns nil
	}

	for _, tt := range tests {
		result := Map(tt.input)
		if tt.input == nil {
			if result != nil {
				t.Error("Expected nil for nil input")
			}
			continue
		}
		if result.Code != tt.expected {
			t.Errorf("Map(%v).Code = %s, want %s", tt.input, result.Code, tt.expected)
		}
	}

	// Test that MemoryError is returned as-is
	memErr := New(CodeConflict, "test")
	mapped := Map(memErr)
	if mapped != memErr {
		t.Error("Expected MemoryError to be returned as-is")
	}
}

func TestErrorUnwrap(t *testing.T) {
	cause := errors.New("root cause")
	err := Wrap(CodeInternal, "wrapper", cause)

	if !errors.Is(err, cause) {
		t.Error("Expected errors.Is to work with wrapped error")
	}
}
