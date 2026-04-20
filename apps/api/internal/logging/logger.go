// Package logging configures the application-wide structured logger (log/slog).
//
// Environment variables:
//
//	KUBEBOLT_LOG_LEVEL    debug | info (default) | warn | error
//	KUBEBOLT_LOG_FORMAT   text (default) | json
//	KUBEBOLT_LOG_DIR      when set, also appends logs to $DIR/kubebolt.log
//	KUBEBOLT_AI_DEBUG     legacy; when "1" forces level=debug for back-compat
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Options controls how the global logger is configured.
type Options struct {
	Level  string
	Format string
	Dir    string    // when non-empty, logs are also appended to $Dir/kubebolt.log
	Output io.Writer // primary output; defaults to os.Stderr
}

// LogFileName is the file the logger appends to when Options.Dir is set.
const LogFileName = "kubebolt.log"

// LoadOptionsFromEnv builds Options from the KUBEBOLT_LOG_* env vars.
// The legacy KUBEBOLT_AI_DEBUG=1 is honored as a shortcut for debug level.
func LoadOptionsFromEnv() Options {
	level := firstNonEmpty(os.Getenv("KUBEBOLT_LOG_LEVEL"), "info")
	if os.Getenv("KUBEBOLT_AI_DEBUG") == "1" {
		level = "debug"
	}
	return Options{
		Level:  level,
		Format: firstNonEmpty(os.Getenv("KUBEBOLT_LOG_FORMAT"), "text"),
		Dir:    os.Getenv("KUBEBOLT_LOG_DIR"),
	}
}

// Setup installs the configured logger as slog.Default() and returns it.
//
// When Options.Dir is set, logs are tee'd to both stderr and $Dir/kubebolt.log.
// If the log file cannot be opened, Setup falls back to stderr-only and emits
// a warning — startup is never blocked by a logging issue.
func Setup(opts Options) *slog.Logger {
	primary := opts.Output
	if primary == nil {
		primary = os.Stderr
	}

	out := primary
	var fileOpenErr error
	var logFilePath string
	if strings.TrimSpace(opts.Dir) != "" {
		f, path, err := openLogFile(opts.Dir)
		if err != nil {
			fileOpenErr = err
		} else {
			logFilePath = path
			out = io.MultiWriter(primary, f)
		}
	}

	handlerOpts := &slog.HandlerOptions{Level: parseLevel(opts.Level)}
	var handler slog.Handler
	switch strings.ToLower(opts.Format) {
	case "json":
		handler = slog.NewJSONHandler(out, handlerOpts)
	default:
		handler = slog.NewTextHandler(out, handlerOpts)
	}
	logger := slog.New(handler)
	slog.SetDefault(logger)

	if fileOpenErr != nil {
		logger.Warn("log file could not be opened, using stderr only",
			slog.String("dir", opts.Dir),
			slog.String("error", fileOpenErr.Error()),
		)
	} else if logFilePath != "" {
		logger.Info("log file enabled", slog.String("path", logFilePath))
	}
	return logger
}

// openLogFile creates the directory if needed and opens (or creates) the log
// file in append mode. The returned file is kept open for the process lifetime;
// the kernel flushes and closes it on exit.
func openLogFile(dir string) (*os.File, string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", fmt.Errorf("create log dir: %w", err)
	}
	path := filepath.Join(dir, LogFileName)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, "", fmt.Errorf("open log file: %w", err)
	}
	return f, path, nil
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "err":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
