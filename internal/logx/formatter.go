package logx

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"
)

// callerFileKey and callerLineKey carry the call site through logrus.Entry.Data
// when a record comes from the slog bridge, whose own frames would otherwise be
// reported by logrus.
const (
	callerFileKey = "logx_caller_file"
	callerLineKey = "logx_caller_line"
)

// formatter renders one record per line as
// "2006-01-02 15:04:05.000 [LEVEL] [file.go:line] message key=value".
type formatter struct{}

func (f *formatter) Format(e *logrus.Entry) ([]byte, error) {
	file, line := caller(e)

	var b bytes.Buffer
	fmt.Fprintf(&b, "%s [%-5s] [%s:%d] %s",
		e.Time.Local().Format("2006-01-02 15:04:05.000"),
		levelName(e.Level), file, line, e.Message)
	writeFields(&b, e.Data)
	b.WriteByte('\n')
	return b.Bytes(), nil
}

// caller resolves the file and line to report, preferring the values injected by
// the slog bridge over logrus' own caller detection.
func caller(e *logrus.Entry) (string, int) {
	file, _ := e.Data[callerFileKey].(string)
	line, _ := e.Data[callerLineKey].(int)
	if file == "" && e.Caller != nil {
		file, line = e.Caller.File, e.Caller.Line
	}
	if file == "" {
		return "???", 0
	}
	return filepath.Base(file), line
}

// writeFields appends the remaining logrus fields as " key=value" pairs, sorted
// for a stable layout.
func writeFields(b *bytes.Buffer, data logrus.Fields) {
	keys := make([]string, 0, len(data))
	for k := range data {
		if k == callerFileKey || k == callerLineKey {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteByte(' ')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(formatValue(data[k]))
	}
}

// formatValue renders a field value on a single line, quoting it when it holds
// spaces so the pairs stay parseable.
func formatValue(v any) string {
	s := fmt.Sprint(v)
	s = strings.ReplaceAll(s, "\n", " ")
	if strings.ContainsAny(s, " \t") {
		return fmt.Sprintf("%q", s)
	}
	return s
}

// levelName renders the level as a short upper case name; logrus itself spells
// the warning level "warning".
func levelName(l logrus.Level) string {
	switch l {
	case logrus.TraceLevel:
		return "TRACE"
	case logrus.DebugLevel:
		return "DEBUG"
	case logrus.WarnLevel:
		return "WARN"
	case logrus.ErrorLevel:
		return "ERROR"
	case logrus.FatalLevel:
		return "FATAL"
	case logrus.PanicLevel:
		return "PANIC"
	default:
		return "INFO"
	}
}
