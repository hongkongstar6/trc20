package logx

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// dailyWriter is an io.Writer that writes to a per-day log file named
// "<service>-YYYY-MM-DD.log" inside dir. It reopens the file when the calendar
// day changes, giving daily rotation without any external dependency. Dates use
// the local time zone so the file name matches the operator's day boundary.
type dailyWriter struct {
	dir     string
	service string

	mu   sync.Mutex
	day  string   // YYYY-MM-DD of the currently open file
	file *os.File // nil until the first successful open
}

func newDailyWriter(dir, service string) *dailyWriter {
	return &dailyWriter{dir: dir, service: service}
}

func (w *dailyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	day := time.Now().Format("2006-01-02") //跨天判断
	if w.file == nil || day != w.day {
		if err := w.rotate(day); err != nil {
			return 0, err
		}
	}
	return w.file.Write(p)
}

// rotate closes the current file (if any) and opens the file for day.
func (w *dailyWriter) rotate(day string) error {
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return err
	}
	name := filepath.Join(w.dir, fmt.Sprintf("%s-%s.log", w.service, day))
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	w.file = f
	w.day = day
	return nil
}

// fileWriter builds the log destination for the given directory and service.
// Output is mirrored to stdout so `docker logs` keeps working while the files
// land on the host. If the directory cannot be written, it falls back to stdout
// only and reports the reason instead of taking the process down.
func fileWriter(dir, service string) io.Writer {
	dw := newDailyWriter(dir, service)
	if _, err := dw.Write(nil); err != nil {
		fmt.Fprintf(os.Stderr, "logx: cannot write to log dir %q: %v; logging to stdout only\n", dir, err)
		return os.Stdout
	}
	return &tee{file: dw, mirror: os.Stdout}
}

// tee writes every record to the log file and mirrors it to stdout. Unlike
// io.MultiWriter it never lets the mirror decide the outcome: a closed or full
// stdout (a detached `docker logs` consumer, for instance) would otherwise stop
// the file from receiving the rest of the record and leave a half written line
// behind.
type tee struct {
	file   io.Writer
	mirror io.Writer
}

func (t *tee) Write(p []byte) (int, error) {
	n, err := t.file.Write(p)
	_, _ = t.mirror.Write(p)
	return n, err
}
