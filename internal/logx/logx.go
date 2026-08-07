// Package logx builds the shared structured logger.
package logx

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/hongkongstar6/trc20/internal/config"
)

// New builds the shared JSON logger. When cfg.LogDir is set, output is written
// to a daily rotating file named "<binary>-YYYY-MM-DD.log" (e.g. api-2026-08-10.log)
// inside that directory and mirrored to stdout; otherwise it goes to stdout only.
// service is attached as a structured attribute on every record.
func New(cfg config.LogConfig, service string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(cfg.Level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	var w io.Writer = os.Stdout
	if dir := strings.TrimSpace(cfg.LogDir); dir != "" {
		w = fileWriter(dir, binaryName())
	}

	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})
	return slog.New(h).With("service", service)
}

// binaryName returns the running executable's base name (e.g. "api"), used as
// the log file prefix so each service writes its own file.
func binaryName() string {
	name := filepath.Base(os.Args[0])
	return strings.TrimSuffix(name, filepath.Ext(name))
}
