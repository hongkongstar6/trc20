package logx

import (
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestFormatKeepsFileText(t *testing.T) {
	f := &formatter{}
	// A node response reaching the message as raw bytes: a NUL and a broken
	// UTF-8 sequence must not end up in the file.
	b, err := f.Format(&logrus.Entry{
		Level:   logrus.ErrorLevel,
		Message: "http 502: \x00bad\xff body\nsecond line\t!",
	})
	if err != nil {
		t.Fatal(err)
	}
	line := string(b)
	if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") {
		t.Fatalf("record must stay on one line: %q", line)
	}
	if strings.ContainsRune(line, 0) {
		t.Fatalf("record still holds a NUL byte: %q", line)
	}
	for _, want := range []string{`\x00`, "\ufffd", `\n`, `\t`} {
		if !strings.Contains(line, want) {
			t.Fatalf("expected %q in %q", want, line)
		}
	}
}

func TestFormatLeavesPlainTextUntouched(t *testing.T) {
	f := &formatter{}
	b, err := f.Format(&logrus.Entry{Level: logrus.InfoLevel, Message: "当前区块读取成功：74212345"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), "当前区块读取成功：74212345\n") {
		t.Fatalf("unexpected line: %q", b)
	}
}
