package logx

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/sirupsen/logrus"
)

// logFile is the daily file InitLogrus writes for the given service.
func logFile(dir, service string) string {
	return filepath.Join(dir, service+"-"+time.Now().Format("2006-01-02")+".log")
}

func TestNewWritesDailyFileInPlainFormat(t *testing.T) {
	dir := t.TempDir()
	InitLogrus(config.LogConfig{Level: "info", LogDir: dir}, "wallet-api")
	logrus.Error("解析参数失败err:invalid character")

	want := logFile(dir, "wallet-api")
	b, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("expected daily log file %q: %v", want, err)
	}
	line := strings.TrimRight(string(b), "\n")

	re := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3} \[ERROR\] \[logx_test\.go:\d+\] 解析参数失败err:invalid character$`)
	if !re.MatchString(line) {
		t.Fatalf("unexpected log line: %q", line)
	}
}

func TestLevelFiltering(t *testing.T) {
	dir := t.TempDir()
	InitLogrus(config.LogConfig{Level: "warn", LogDir: dir}, "svc")
	logrus.Info("dropped")
	logrus.Warn("kept")

	b, err := os.ReadFile(logFile(dir, "svc"))
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if strings.Contains(out, "dropped") {
		t.Fatalf("info record should be filtered out: %s", out)
	}
	if !strings.Contains(out, "[WARNING]") || !strings.Contains(out, "kept") {
		t.Fatalf("warn record missing or misformatted: %s", out)
	}
}

// A log file has to stay readable as text: a record carrying raw node bytes
// must not turn the whole file into something an editor refuses to display.
func TestFileStaysValidUTF8(t *testing.T) {
	dir := t.TempDir()
	InitLogrus(config.LogConfig{LogDir: dir}, "scanner")
	logrus.Error("head: http 502: \x00\xff\xfe")

	b, err := os.ReadFile(logFile(dir, "scanner"))
	if err != nil {
		t.Fatal(err)
	}
	if !isPlainText(strings.TrimRight(string(b), "\n")) {
		t.Fatalf("log file is not plain text: %q", b)
	}
}

func TestNewStdoutOnlyWhenNoDir(t *testing.T) {
	// No LogDir: must not create files anywhere; just exercise the path.
	InitLogrus(config.LogConfig{Level: "debug"}, "svc")
	logrus.Debug("noop")
}
