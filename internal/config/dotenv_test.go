package config

import (
	"os"
	"path/filepath"
	"testing"
)

const minimalYAML = "network: nile\nmysql_server:\n  dsn: ${MYSQL_DSN}\nscanner_server:\n  chain_nodes:\n    - name: nile\n      type: fullnode\n      endpoint: https://nile.trongrid.io\n      enabled: true\n" + usdtTokens

func TestLoadDotEnvParsesAndKeepsExistingEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# comment\n\nexport A=1\nB = plain value # trailing\nC=\"quoted \\\"x\\\"\"\nD='raw'\nALREADY=fromfile\nnovalue\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ALREADY", "fromenv")
	for _, k := range []string{"A", "B", "C", "D"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	want := map[string]string{"A": "1", "B": "plain value", "C": `quoted "x"`, "D": "raw", "ALREADY": "fromenv"}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

// A value that is nothing but a comment must stay empty: an accidental
// "WALLET_PASSPHRASE= #口令" used to become a real passphrase and silently
// changed every derived address.
func TestParseDotEnvLineComments(t *testing.T) {
	cases := map[string]string{
		"WALLET_PASSPHRASE= #口令":  "",
		"WALLET_PASSPHRASE=#口令":   "",
		"WALLET_PASSPHRASE=\t# x": "",
		"P=pa#ss":                 "pa#ss",
		"P=value\t# note":         "value",
	}
	for line, want := range cases {
		_, got, ok := parseDotEnvLine(line)
		if !ok {
			t.Fatalf("%q was skipped", line)
		}
		if got != want {
			t.Fatalf("%q -> %q, want %q", line, got, want)
		}
	}
}

func TestLoadDotEnvMissingFileIsNoError(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), ".env")); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
}

// Load must source the .env sitting next to (or above) the config file so the
// ${VAR} references resolve when debugging from an IDE.
func TestLoadSourcesDotEnvNextToConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("MYSQL_DSN=user:pass@tcp(127.0.0.1:3306)/wallet\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(minimalYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", "")
	os.Unsetenv("ENV_FILE")
	t.Setenv("MYSQL_DSN", "")
	os.Unsetenv("MYSQL_DSN")

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MySQLCf.DSN != "user:pass@tcp(127.0.0.1:3306)/wallet" {
		t.Fatalf("dsn = %q", cfg.MySQLCf.DSN)
	}
}

func TestLoadHonorsEnvFileOverride(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "custom.env")
	if err := os.WriteFile(envPath, []byte("MYSQL_DSN=from-custom\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(minimalYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", envPath)
	t.Setenv("MYSQL_DSN", "")
	os.Unsetenv("MYSQL_DSN")

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MySQLCf.DSN != "from-custom" {
		t.Fatalf("dsn = %q", cfg.MySQLCf.DSN)
	}
}
