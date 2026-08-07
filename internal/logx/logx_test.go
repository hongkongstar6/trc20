package logx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hongkongstar6/trc20/internal/config"
)

func TestNewWritesDailyFile(t *testing.T) {
	dir := t.TempDir()
	log := New(config.LogConfig{Level: "info", LogDir: dir}, "wallet-api")
	log.Info("hello", "k", "v")

	want := filepath.Join(dir, binaryName()+"-"+time.Now().Format("2006-01-02")+".log")
	b, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("expected daily log file %q: %v", want, err)
	}
	if !strings.Contains(string(b), `"service":"wallet-api"`) || !strings.Contains(string(b), `"msg":"hello"`) {
		t.Fatalf("log record missing expected fields: %s", b)
	}
}

func TestNewStdoutOnlyWhenNoDir(t *testing.T) {
	// No LogDir: must not create files anywhere; just exercise the path.
	log := New(config.LogConfig{Level: "debug"}, "svc")
	log.Debug("noop")
}
