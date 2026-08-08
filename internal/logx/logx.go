// Package logx builds the shared logger. Records are emitted through logrus in
// a fixed plain-text layout: "2006-01-02 15:04:05.000 [LEVEL] [file.go:line] message".
package logx

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/hongkongstar6/trc20/internal/config"
)

// New builds the shared logger. When cfg.LogDir is set, output is written to a
// daily rotating file named "<binary>-YYYY-MM-DD.log" (e.g. api-2026-08-10.log)
// inside that directory and mirrored to stdout; otherwise it goes to stdout only.
// service is attached as a field on every record.

// func New(cfg config.LogConfig, service string) *slog.Logger {
// 	return slog.New(newHandler(NewLogrus(cfg, service), level(cfg.Level))).With("service", service)
// }

// func New(cfg config.LogConfig, service string) *logrus.Logger {
// 	//return slog.New(newHandler(NewLogrus(cfg, service), level(cfg.Level))).With("service", service)
// 	return NewLogrus(cfg, service)
// }

// NewLogrus builds the underlying logrus logger, for code that wants the logrus
// API directly instead of the slog facade returned by New.
//
//	func NewLogrus(cfg config.LogConfig, name string) *logrus.Logger {
//		l := logrus.StandardLogger()
//		l.SetOutput(writer(cfg, name))
//		l.SetFormatter(&formatter{})
//		l.SetLevel(logrusLevel(level(cfg.Level)))
//		return l
//	}
func InitLogrus(cfg config.LogConfig, name string) {
	dir := strings.TrimSpace(cfg.LogDir)
	dw := newDailyWriter(dir, name)

	logrus.SetOutput(dw)
	logrus.SetReportCaller(true) //显示行号和函数名
	logrus.SetFormatter(&formatter{})
	logrus.SetLevel(logrusLevel(level(cfg.Level)))
	//return logrus.StandardLogger()
	//return nil
}

// func writer(cfg config.LogConfig, name string) io.Writer {
// 	if dir := strings.TrimSpace(cfg.LogDir); dir != "" {
// 		return fileWriter(dir, name)
// 	}
// 	return os.Stdout
// }

func level(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// binaryName returns the running executable's base name (e.g. "api"), used as
// the log file prefix so each service writes its own file.
func binaryName() string {
	name := filepath.Base(os.Args[0])
	return strings.TrimSuffix(name, filepath.Ext(name))
}
