package logx

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hongkongstar6/trc20/internal/config"
)

func TestNewWritesDailyFileInPlainFormat(t *testing.T) {
	dir := t.TempDir()
	log := NewLogrus(config.LogConfig{Level: "info", LogDir: dir}, "wallet-api")
	log.Error("解析参数失败err:invalid character", "txid", "abc")

	want := filepath.Join(dir, binaryName()+"-"+time.Now().Format("2006-01-02")+".log")
	b, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("expected daily log file %q: %v", want, err)
	}
	line := strings.TrimRight(string(b), "\n")

	re := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3} \[ERROR\] \[logx_test\.go:\d+\] 解析参数失败err:invalid character service=wallet-api txid=abc$`)
	if !re.MatchString(line) {
		t.Fatalf("unexpected log line: %q", line)
	}
}

func TestLevelFiltering(t *testing.T) {
	dir := t.TempDir()
	log := NewLogrus(config.LogConfig{Level: "warn", LogDir: dir}, "svc")
	log.Info("dropped")
	log.Warn("kept")

	b, err := os.ReadFile(filepath.Join(dir, binaryName()+"-"+time.Now().Format("2006-01-02")+".log"))
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if strings.Contains(out, "dropped") {
		t.Fatalf("info record should be filtered out: %s", out)
	}
	if !strings.Contains(out, "[WARN ] ") || !strings.Contains(out, "kept") {
		t.Fatalf("warn record missing or misformatted: %s", out)
	}
}

func TestGroupedAttrsUseDottedKeys(t *testing.T) {
	dir := t.TempDir()
	//log := New(config.LogConfig{LogDir: dir}, "svc").WithGroup("chain").With("block", 42)
	log := NewLogrus(config.LogConfig{LogDir: dir}, "svc")
	log.Info("scanned")

	b, err := os.ReadFile(filepath.Join(dir, binaryName()+"-"+time.Now().Format("2006-01-02")+".log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "chain.block=42") {
		t.Fatalf("expected dotted group key: %s", b)
	}
}

func TestNewStdoutOnlyWhenNoDir(t *testing.T) {
	// No LogDir: must not create files anywhere; just exercise the path.
	log := NewLogrus(config.LogConfig{Level: "debug"}, "svc")
	log.Debug("noop")
}
