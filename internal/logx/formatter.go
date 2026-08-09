package logx

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

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
	var file string
	var line int
	//file, line := caller(e)
	if e.Caller != nil {
		file = filepath.Base(e.Caller.File)
		line = e.Caller.Line
	}
	timestamp := time.Now().Local().Format("2006-01-02 15:04:05.000")

	var b bytes.Buffer

	//msg := fmt.Sprintf("%s [%-5s] [%s:%d] %s\n", timestamp, strings.ToUpper(e.Level.String()), file, line, e.Message)

	// fmt.Fprintf(&b, "%s [%-5s] [%s:%d] %s", timestamp, levelName(e.Level), file, line, e.Message)
	// writeFields(&b, e.Data)
	// b.WriteByte('\n')
	// 格式化日志信息
	msg := fmt.Sprintf("%s [%-5s] [%s:%d] %s\n", timestamp, strings.ToUpper(e.Level.String()), file, line, sanitize(e.Message))
	b.WriteString(msg)
	return b.Bytes(), nil
}

// sanitize keeps a record printable as text: node responses and chain error
// payloads reach the message as raw bytes, and a single NUL or an invalid UTF-8
// sequence makes an editor treat the whole log file as binary. Invalid bytes
// become U+FFFD and control characters are escaped, so one record still is one
// readable line.
func sanitize(s string) string {
	if isPlainText(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteRune(utf8.RuneError)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// isPlainText reports whether s can be written as is: valid UTF-8 without
// control characters. Virtually every record takes this path.
func isPlainText(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f {
			return false
		}
	}
	return utf8.ValidString(s)
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
