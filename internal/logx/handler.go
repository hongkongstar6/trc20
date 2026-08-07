package logx

import (
	"context"
	"log/slog"
	"runtime"

	"github.com/sirupsen/logrus"
)

// handler is a slog.Handler that emits every record through logrus, so call
// sites keep the slog API while the output format is owned by logrus.
type handler struct {
	log    *logrus.Logger
	level  slog.Level
	fields logrus.Fields
	prefix string // dot separated group path applied to attribute keys
}

func newHandler(log *logrus.Logger, level slog.Level) slog.Handler {
	return &handler{log: log, level: level, fields: logrus.Fields{}}
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	fields := make(logrus.Fields, len(h.fields)+r.NumAttrs()+2)
	for k, v := range h.fields {
		fields[k] = v
	}
	r.Attrs(func(a slog.Attr) bool {
		putAttr(fields, h.prefix, a)
		return true
	})
	if file, line := frame(r.PC); file != "" {
		fields[callerFileKey] = file
		fields[callerLineKey] = line
	}

	h.log.WithTime(r.Time).WithFields(fields).Log(logrusLevel(r.Level), r.Message)
	return nil
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	next := h.clone()
	for _, a := range attrs {
		putAttr(next.fields, next.prefix, a)
	}
	return next
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := h.clone()
	next.prefix += name + "."
	return next
}

func (h *handler) clone() *handler {
	fields := make(logrus.Fields, len(h.fields))
	for k, v := range h.fields {
		fields[k] = v
	}
	return &handler{log: h.log, level: h.level, fields: fields, prefix: h.prefix}
}

// putAttr flattens attr into fields, expanding groups into dotted keys.
func putAttr(fields logrus.Fields, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		group := a.Value.Group()
		if a.Key != "" {
			prefix += a.Key + "."
		}
		for _, sub := range group {
			putAttr(fields, prefix, sub)
		}
		return
	}
	fields[prefix+a.Key] = a.Value.Any()
}

// frame resolves the call site recorded by slog.
func frame(pc uintptr) (string, int) {
	if pc == 0 {
		return "", 0
	}
	f, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	return f.File, f.Line
}

func logrusLevel(l slog.Level) logrus.Level {
	switch {
	case l <= slog.LevelDebug:
		return logrus.DebugLevel
	case l < slog.LevelWarn:
		return logrus.InfoLevel
	case l < slog.LevelError:
		return logrus.WarnLevel
	default:
		return logrus.ErrorLevel
	}
}
