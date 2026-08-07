package logx

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDailyWriterFileName(t *testing.T) {
	dir := t.TempDir()
	w := newDailyWriter(dir, "api")
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	want := filepath.Join(dir, "api-"+time.Now().Format("2006-01-02")+".log")
	b, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("expected log file %q: %v", want, err)
	}
	if string(b) != "hello\n" {
		t.Fatalf("unexpected contents: %q", b)
	}
}

func TestDailyWriterRotatesOnDayChange(t *testing.T) {
	dir := t.TempDir()
	w := newDailyWriter(dir, "api")

	// Open an explicit "yesterday" file, then let a normal Write detect the
	// day change and roll over to today's file.
	if err := w.rotate("2000-01-01"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := w.file.WriteString("day1\n"); err != nil {
		t.Fatalf("write day1: %v", err)
	}
	if _, err := w.Write([]byte("day2\n")); err != nil {
		t.Fatalf("write day2: %v", err)
	}
	today := time.Now().Format("2006-01-02")
	if w.day != today {
		t.Fatalf("expected rotation to today %q, got %q", today, w.day)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected 2 daily files, got %v", names)
	}
}
