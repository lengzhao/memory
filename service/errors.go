// Package service 内的结构化错误，替代独立 pkg/errors，避免多一层 public surface。
package service

import (
	"errors"
	"fmt"
)

// 标准 sentinels（与 GORM/业务里 errors.Is 配合）
var (
	ErrNotFound     = errors.New("item not found")
	ErrConflict     = errors.New("version conflict")
	ErrDuplicate    = errors.New("duplicate item")
	ErrValidation   = errors.New("validation error")
	ErrUnauthorized = errors.New("unauthorized")
	ErrLLM          = errors.New("llm error")
)

// ErrorCode 用于可编程分支。
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

// MemoryError 带 code / 链式 unwrap。
type MemoryError struct {
	Code    ErrorCode
	Message string
	Cause   error
	Retry   bool
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

func isRetryable(code ErrorCode) bool {
	switch code {
	case CodeConflict, CodeLLM:
		return true
	default:
		return false
	}
}

// newErr 与 wrapErr 供本包 service 使用。
func newErr(code ErrorCode, message string) *MemoryError {
	return &MemoryError{Code: code, Message: message, Retry: isRetryable(code)}
}

func wrapErr(code ErrorCode, message string, cause error) *MemoryError {
	return &MemoryError{Code: code, Message: message, Cause: cause, Retry: isRetryable(code)}
}

// ErrorNew 与 ErrorWrap、ErrorAs 供根包重导出，或需直接 import service 的调用方使用。
func ErrorNew(code ErrorCode, message string) *MemoryError {
	return newErr(code, message)
}

func ErrorWrap(code ErrorCode, message string, cause error) *MemoryError {
	return wrapErr(code, message, cause)
}

// ErrorAs 从错误链取出 *MemoryError（兼容 errors.As）。
func ErrorAs(err error) (*MemoryError, bool) {
	var me *MemoryError
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}
