// Package errors defines standard error types for the memory system.
package errors

import (
	"errors"
	"fmt"
)

// Standard error types for the memory system.
var (
	// ErrNotFound indicates the requested item does not exist.
	ErrNotFound = errors.New("item not found")

	// ErrConflict indicates an optimistic locking conflict (version mismatch).
	ErrConflict = errors.New("version conflict")

	// ErrDuplicate indicates a duplicate item (e.g., same dedupe_key).
	ErrDuplicate = errors.New("duplicate item")

	// ErrValidation indicates invalid input parameters.
	ErrValidation = errors.New("validation error")

	// ErrUnauthorized indicates missing or invalid credentials.
	ErrUnauthorized = errors.New("unauthorized")

	// ErrLLM indicates an error from the LLM provider.
	ErrLLM = errors.New("llm error")
)

// ErrorCode represents a structured error code for programmatic handling.
type ErrorCode string

const (
	CodeNotFound     ErrorCode = "NOT_FOUND"
	CodeConflict     ErrorCode = "CONFLICT"
	CodeDuplicate    ErrorCode = "DUPLICATE"
	CodeValidation   ErrorCode = "VALIDATION"
	CodeUnauthorized ErrorCode = "UNAUTHORIZED"
	CodeLLM          ErrorCode = "LLM_ERROR"
	CodeInternal     ErrorCode = "INTERNAL"
)

// MemoryError provides structured error information.
type MemoryError struct {
	Code    ErrorCode
	Message string
	Cause   error
	Retry   bool // Whether the operation can be retried
}

func (e *MemoryError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *MemoryError) Unwrap() error {
	return e.Cause
}

// New creates a new MemoryError.
func New(code ErrorCode, message string) *MemoryError {
	return &MemoryError{
		Code:    code,
		Message: message,
		Retry:   IsRetryable(code),
	}
}

// Wrap wraps an existing error with a code.
func Wrap(code ErrorCode, message string, cause error) *MemoryError {
	return &MemoryError{
		Code:    code,
		Message: message,
		Cause:   cause,
		Retry:   IsRetryable(code),
	}
}

// IsRetryable returns whether an error code indicates a retryable operation.
func IsRetryable(code ErrorCode) bool {
	switch code {
	case CodeConflict, CodeLLM:
		return true
	default:
		return false
	}
}

// Is checks if an error matches the target error.
func Is(err, target error) bool {
	return errors.Is(err, target)
}

// As attempts to extract a MemoryError from an error chain.
func As(err error) (*MemoryError, bool) {
	var me *MemoryError
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}

// Map maps standard errors to MemoryError.
func Map(err error) *MemoryError {
	if err == nil {
		return nil
	}

	// Check if already a MemoryError
	if me, ok := As(err); ok {
		return me
	}

	// Map standard errors
	switch {
	case errors.Is(err, ErrNotFound):
		return Wrap(CodeNotFound, "item not found", err)
	case errors.Is(err, ErrConflict):
		return Wrap(CodeConflict, "version conflict, please retry", err)
	case errors.Is(err, ErrDuplicate):
		return Wrap(CodeDuplicate, "item already exists", err)
	case errors.Is(err, ErrValidation):
		return Wrap(CodeValidation, "invalid input", err)
	case errors.Is(err, ErrUnauthorized):
		return Wrap(CodeUnauthorized, "unauthorized", err)
	case errors.Is(err, ErrLLM):
		return Wrap(CodeLLM, "llm service error", err)
	default:
		return Wrap(CodeInternal, "internal error", err)
	}
}
