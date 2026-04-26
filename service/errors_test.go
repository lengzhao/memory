package service

import (
	"errors"
	"testing"
)

func TestErrorNew(t *testing.T) {
	err := newErr(CodeNotFound, "item not found")
	if err.Code != CodeNotFound {
		t.Errorf("code = %s, want %s", err.Code, CodeNotFound)
	}
	if err.Retry {
		t.Error("Retry for NOT_FOUND should be false")
	}
}

func TestWrapErr(t *testing.T) {
	cause := errors.New("underlying")
	err := wrapErr(CodeInternal, "op failed", cause)
	if err.Code != CodeInternal {
		t.Errorf("code = %s", err.Code)
	}
	if err.Cause != cause {
		t.Error("expected cause set")
	}
}

func TestErrorAs(t *testing.T) {
	inner := newErr(CodeNotFound, "x")
	got, ok := ErrorAs(inner)
	if !ok || got.Code != CodeNotFound {
		t.Error("ErrorAs on MemoryError")
	}
	w := wrapErr(CodeInternal, "w", inner)
	_, _ = w, inner
	// 外层包装后链上应用 errors.As
	var me *MemoryError
	if !errors.As(w, &me) {
		t.Fatal("errors.As on wrapped")
	}
	if me.Code != CodeInternal {
		t.Error("outer code")
	}
}

func TestErrorUnwrap(t *testing.T) {
	cause := errors.New("root")
	err := wrapErr(CodeInternal, "wrap", cause)
	if !errors.Is(err, cause) {
		t.Error("errors.Is with unwrap")
	}
}

func TestErrorNewExported(t *testing.T) {
	err := ErrorNew(CodeConflict, "c")
	if err.Code != CodeConflict {
		t.Error(err)
	}
}

func TestErrorWrapExported(t *testing.T) {
	err := ErrorWrap(CodeValidation, "v", errors.New("e"))
	if err.Code != CodeValidation {
		t.Error(err)
	}
}
