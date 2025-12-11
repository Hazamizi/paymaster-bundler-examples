package logger

import (
	"log/slog"
	"os"

	"github.com/ethereum/go-ethereum/log"
)

// ConfigureLogger sets up the logger with Datadog-compatible JSON format
func ConfigureLogger(level string, debug bool) {
	// Parse log level
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "info":
		logLevel = slog.LevelInfo
	default:
		logLevel = slog.LevelInfo
	}

	// Create a JSON handler with level
	handler := log.JSONHandlerWithLevel(os.Stdout, logLevel)

	// Set the default logger
	log.SetDefault(log.NewLogger(handler))
}
