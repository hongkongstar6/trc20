package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const minimalConfig = `
mysql:
  dsn: "user:pass@tcp(127.0.0.1:3306)/wallet"
chain:
  nodes:
    - name: fullnode
      type: fullnode
      endpoint: http://127.0.0.1:8090
      priority: 1
      enabled: true
`

func TestLoadAppliesSafeDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Nile by default: mainnet must be opted into explicitly.
	if cfg.Network != "nile" {
		t.Fatalf("network = %s, want nile", cfg.Network)
	}
	if cfg.Deposit.Confirmations != 19 {
		t.Fatalf("confirmations = %d, want 19", cfg.Deposit.Confirmations)
	}
	if cfg.Wallet.AccountPath != "m/44'/195'/0'/0" {
		t.Fatalf("account path = %s", cfg.Wallet.AccountPath)
	}
	if cfg.Energy.Mode != "cheapest" {
		t.Fatalf("energy mode = %s", cfg.Energy.Mode)
	}
	if cfg.Energy.EnergyPerTx != 32000 || cfg.Energy.EnergyPerTxNew != 65000 {
		t.Fatalf("energy estimates = %d/%d", cfg.Energy.EnergyPerTx, cfg.Energy.EnergyPerTxNew)
	}
	// Automatic provider funding must never be on unless it is asked for.
	if cfg.Energy.AutoTopup.Enabled {
		t.Fatal("auto topup must default to disabled")
	}
}

func TestLoadExpandsEnvReferences(t *testing.T) {
	t.Setenv("TEST_WALLET_DSN", "secret-dsn")
	cfg, err := Load(writeConfig(t, `
mysql:
  dsn: "${TEST_WALLET_DSN}"
redis:
  addr: "${TEST_UNSET_REDIS:-127.0.0.1:6379}"
chain:
  nodes:
    - name: fullnode
      endpoint: http://127.0.0.1:8090
      enabled: true
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MySQL.DSN != "secret-dsn" {
		t.Fatalf("dsn = %q, want the environment value", cfg.MySQL.DSN)
	}
	if cfg.Redis.Addr != "127.0.0.1:6379" {
		t.Fatalf("redis addr = %q, want the inline default", cfg.Redis.Addr)
	}
}

// An unset reference without a default must expand to empty and then trip
// validation, rather than leaving the literal "${VAR}" in a connection string.
func TestLoadUnsetEnvBecomesEmpty(t *testing.T) {
	_, err := Load(writeConfig(t, `
mysql:
  dsn: "${TEST_DEFINITELY_UNSET_DSN}"
chain:
  nodes:
    - name: fullnode
      enabled: true
`))
	if err == nil {
		t.Fatal("expected the missing dsn to fail validation")
	}
}

// An exported but empty variable must still fall back to the ${VAR:-default}
// value, matching POSIX semantics; otherwise an empty MYSQL_DSN=... in the
// environment silently blanks the dsn and trips "mysql.dsn is required".
func TestLoadEmptyEnvFallsBackToDefault(t *testing.T) {
	t.Setenv("MYSQL_DSN", "")
	cfg, err := Load(writeConfig(t, `
mysql:
  dsn: "${MYSQL_DSN:-user:pass@tcp(127.0.0.1:3306)/wallet}"
chain:
  nodes:
    - name: fullnode
      enabled: true
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MySQL.DSN != "user:pass@tcp(127.0.0.1:3306)/wallet" {
		t.Fatalf("dsn = %q, want the inline default", cfg.MySQL.DSN)
	}
}

func TestValidateRequiresAnEnabledNode(t *testing.T) {
	_, err := Load(writeConfig(t, `
mysql:
  dsn: "dsn"
chain:
  nodes:
    - name: fullnode
      enabled: false
`))
	if err == nil {
		t.Fatal("a config with no usable node must be rejected at startup")
	}
}

func TestValidateFixedModeNeedsAProvider(t *testing.T) {
	_, err := Load(writeConfig(t, minimalConfig+`
energy:
  mode: fixed
`))
	if err == nil {
		t.Fatal("energy.mode=fixed without energy.fixed must be rejected")
	}
}

// Auto topup without a hard coded deposit address would let whatever the
// provider API returns receive TRX.
func TestValidateAutoTopupNeedsWhitelistedDepositAddress(t *testing.T) {
	_, err := Load(writeConfig(t, minimalConfig+`
energy:
  auto_topup:
    enabled: true
    source_address: TZJ9qkoxUB1SGdbtChgAjUphBmkJwAeBaW
    providers:
      gasstation:
        enabled: true
`))
	if err == nil {
		t.Fatal("an enabled provider without a deposit address must be rejected")
	}
}

func TestValidateAutoTopupNeedsSourceAddress(t *testing.T) {
	_, err := Load(writeConfig(t, minimalConfig+`
energy:
  auto_topup:
    enabled: true
`))
	if err == nil {
		t.Fatal("auto topup without a gas account source must be rejected")
	}
}

func TestNodePriorityAndPeriodsSurvive(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
mysql:
  dsn: "dsn"
chain:
  nodes:
    - name: trongrid
      type: trongrid
      endpoint: https://nile.trongrid.io
      priority: 2
      enabled: true
    - name: fullnode
      type: fullnode
      endpoint: http://127.0.0.1:8090
      priority: 1
      enabled: true
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Chain.Nodes) != 2 {
		t.Fatalf("nodes = %d", len(cfg.Chain.Nodes))
	}
	// The gateway sorts by priority itself; the loader must keep the values.
	if cfg.Chain.Nodes[0].Priority != 2 || cfg.Chain.Nodes[1].Priority != 1 {
		t.Fatalf("priorities = %d/%d", cfg.Chain.Nodes[0].Priority, cfg.Chain.Nodes[1].Priority)
	}
}

func TestDurationFallsBackOnBadInput(t *testing.T) {
	if got := Duration("30s", time.Minute); got != 30*time.Second {
		t.Fatalf("got %v", got)
	}
	for _, s := range []string{"", "abc", "0s", "-5s"} {
		if got := Duration(s, time.Minute); got != time.Minute {
			t.Fatalf("Duration(%q) = %v, want the default", s, got)
		}
	}
}

// The committed Nile config must load and must not reach for a mainnet rental
// platform: neither has a test environment.
func TestCommittedNileConfigLoads(t *testing.T) {
	t.Setenv("MYSQL_DSN", "user:pass@tcp(127.0.0.1:3306)/wallet")
	cfg, err := Load(filepath.Join("..", "..", "configs", "config.nile.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Network != "nile" {
		t.Fatalf("network = %s", cfg.Network)
	}
	if cfg.Energy.Mode != "fixed" || cfg.Energy.Fixed != "trx_burn" {
		t.Fatalf("energy = %s/%s, want fixed/trx_burn on Nile", cfg.Energy.Mode, cfg.Energy.Fixed)
	}
	for _, name := range []string{"gasstation", "tronenergyrent"} {
		if cfg.Energy.Providers[name].Enabled {
			t.Fatalf("%s must stay disabled on Nile", name)
		}
	}
	if cfg.Energy.AutoTopup.Enabled {
		t.Fatal("auto topup must stay disabled in the committed config")
	}
}

// An env value carrying yaml metacharacters must stay a plain string instead of
// breaking the document: a mnemonic or a password with quotes, '#' or a newline
// used to make the whole file fail with "did not find expected key".
func TestLoadEnvValueWithYAMLMetacharacters(t *testing.T) {
	t.Setenv("TEST_WALLET_DSN", "user:p\"a#ss\nword@tcp(127.0.0.1:3306)/wallet")
	t.Setenv("TEST_NODE_ENDPOINT", "http://node:8090 # not a comment")
	cfg, err := Load(writeConfig(t, `
mysql:
  dsn: "${TEST_WALLET_DSN}"
chain:
  nodes:
    - name: fullnode
      endpoint: "${TEST_NODE_ENDPOINT}"
      enabled: true
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MySQL.DSN != "user:p\"a#ss\nword@tcp(127.0.0.1:3306)/wallet" {
		t.Fatalf("dsn = %q, want the raw environment value", cfg.MySQL.DSN)
	}
	if cfg.Chain.Nodes[0].Endpoint != "http://node:8090 # not a comment" {
		t.Fatalf("endpoint = %q", cfg.Chain.Nodes[0].Endpoint)
	}
}

// Plain (unquoted) references must keep their yaml type, so numbers and booleans
// coming from the environment still decode into int / bool fields.
func TestLoadPlainEnvReferenceKeepsType(t *testing.T) {
	t.Setenv("TEST_REDIS_DB", "3")
	t.Setenv("TEST_NODE_ENABLED", "true")
	cfg, err := Load(writeConfig(t, `
mysql:
  dsn: "dsn"
redis:
  db: ${TEST_REDIS_DB}
chain:
  nodes:
    - name: fullnode
      enabled: ${TEST_NODE_ENABLED}
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Redis.DB != 3 {
		t.Fatalf("redis db = %d, want 3", cfg.Redis.DB)
	}
	if !cfg.Chain.Nodes[0].Enabled {
		t.Fatal("node must be enabled")
	}
}

// A real syntax error must report the offending source line.
func TestLoadSyntaxErrorMentionsTheLine(t *testing.T) {
	_, err := Load(writeConfig(t, "mysql:\n  dsn: dsn\n   bad: 1\n"))
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if !strings.Contains(err.Error(), "bad: 1") {
		t.Fatalf("error = %v, want the offending line quoted", err)
	}
}

// The shipped configs/config.yaml is the file baked into the docker image, so
// docker compose's MYSQL_DSN must win over the value written in it.
func TestShippedConfigHonorsMySQLDSNEnv(t *testing.T) {
	path := filepath.Join("..", "..", "configs", "config.yaml")
	t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "absent.env"))
	t.Setenv("MYSQL_DSN", "wallet:wallet@tcp(mysql:3306)/wallet?charset=utf8mb4&parseTime=True&loc=Local")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MySQL.DSN != os.Getenv("MYSQL_DSN") {
		t.Fatalf("mysql dsn = %q, want the MYSQL_DSN value", cfg.MySQL.DSN)
	}

	os.Unsetenv("MYSQL_DSN")
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("Load without MYSQL_DSN: %v", err)
	}
	if !strings.Contains(cfg.MySQL.DSN, "127.0.0.1:3306") {
		t.Fatalf("fallback dsn = %q, want the in-file default", cfg.MySQL.DSN)
	}
}
