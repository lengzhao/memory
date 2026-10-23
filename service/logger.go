// Package service provides memory extraction services using LLM.
package service

import (
	"log/slog"
	"os"
)

// logger is the package-level logger instance.
//
// It defaults to a WARN-level stderr handler: a library should stay silent in a host's log
// unless something is wrong. Previously it logged at INFO, so every extraction printed
// "memories persisted" into the host's output. Hosts that want more detail call SetLogger.
var logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
	Level: slog.LevelWarn,
}))

// SetLogger sets the package-level logger (useful for tests)
func SetLogger(l *slog.Logger) {
	logger = l
}

// GetLogger returns the package-level logger instance
func GetLogger() *slog.Logger {
	return logger
}
