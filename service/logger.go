// Package service provides memory extraction services using LLM.
package service

import (
	"log/slog"
	"os"
)

// logger is the package-level logger instance
var logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
	Level: slog.LevelInfo,
}))

// SetLogger sets the package-level logger (useful for tests)
func SetLogger(l *slog.Logger) {
	logger = l
}

// GetLogger returns the package-level logger instance
func GetLogger() *slog.Logger {
	return logger
}
