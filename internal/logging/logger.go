package logging

import (
	"log/slog"
	"os"
)

// New builds a text-handler slog.Logger writing to stderr at the given level
// ("debug", "info", "warn", "error"; unrecognized values fall back to "info").
func New(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}
