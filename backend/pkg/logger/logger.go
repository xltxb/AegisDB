// Package logger configures the structured (slog) logger used across the service.
package logger

import (
	"log/slog"
	"os"
)

// Setup installs a JSON structured logger as the slog default.
func Setup(mode string) *slog.Logger {
	level := slog.LevelInfo
	if mode == "debug" {
		level = slog.LevelDebug
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	l := slog.New(h)
	slog.SetDefault(l)
	return l
}
