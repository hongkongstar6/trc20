package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LoadDotEnv reads a .env file and exports the entries that are not already
// present in the process environment, so a real environment variable always
// wins over the file. A missing file is not an error: production passes the
// values through the environment instead.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, value, ok := parseDotEnvLine(sc.Text())
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return sc.Err()
}

func parseDotEnvLine(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")
	k, v, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}
	k = strings.TrimSpace(k)
	if k == "" {
		return "", "", false
	}
	v = strings.TrimSpace(v)
	switch {
	case len(v) >= 2 && strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`):
		v = strings.NewReplacer(`\n`, "\n", `\"`, `"`, `\\`, `\`).Replace(v[1 : len(v)-1])
	case len(v) >= 2 && strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'"):
		v = v[1 : len(v)-1]
	default:
		v = stripInlineComment(v)
	}
	return k, v, true
}

// stripInlineComment drops the trailing comment of an unquoted value. The
// comment marker counts when it opens the value or follows whitespace, so
// "WALLET_PASSPHRASE= #口令" stays empty instead of becoming the comment itself,
// while a value such as "pa#ss" keeps its '#'.
func stripInlineComment(v string) string {
	for i, r := range v {
		if r != '#' {
			continue
		}
		if i == 0 {
			return ""
		}
		prev, _ := utf8.DecodeLastRuneInString(v[:i])
		if unicode.IsSpace(prev) {
			return strings.TrimSpace(v[:i])
		}
	}
	return v
}

// FindUp walks from dir towards the filesystem root and returns the first
// existing dir/name. It lets `go run`, `go test` and the VS Code debugger all
// resolve repository level files regardless of their working directory.
func FindUp(dir, name string) (string, bool) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", false
		}
		dir = wd
	}
	for {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
