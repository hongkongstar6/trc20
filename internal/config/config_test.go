package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// usdtTokens is the token allowlist every config needs to pass validation. It
// is appended to a fixture that does not carry a wallet section of its own, so
// the fixtures stay focused on what they actually assert.
const usdtTokens = `
wallet:
  tokens:
    - symbol: USDT
      contract: TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t
      decimals: 6
      enabled: true
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	if !strings.Contains(body, "\nwallet:") {
		body += usdtTokens
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const minimalConfig = `
mysql_server:
  dsn: "user:pass@tcp(127.0.0.1:3306)/wallet"
scanner_server:
  chain_nodes:
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

// The tokens allowlist decides which contract logs can be a deposit at all: an
// empty one makes the scanner drop every transfer on chain without any error,
// so a config section that no struct field maps to must fail at startup.
func TestLoadRejectsUnknownSection(t *testing.T) {
	_, err := Load(writeConfig(t, minimalConfig+`
wallet_server:
  tokens:
    - symbol: USDT
      contract: TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t
      decimals: 6
      enabled: true
`))
	if err == nil || !strings.Contains(err.Error(), "unknown section") {
		t.Fatalf("error = %v, want an unknown section error", err)
	}
}

func TestValidateRequiresAnEnabledToken(t *testing.T) {
	_, err := Load(writeConfig(t, minimalConfig+`
wallet:
  tokens:
    - symbol: USDT
      contract: TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t
      decimals: 6
      enabled: false
`))
	if err == nil {
		t.Fatal("a config without an enabled token must be rejected: the scanner would match nothing")
	}
}

// Every shipped config must produce the USDT allowlist the scanner matches on.
func TestCommittedConfigsCarryTheUSDTAllowlist(t *testing.T) {
	t.Setenv("MYSQL_DSN", "user:pass@tcp(127.0.0.1:3306)/wallet")
	for _, name := range []string{"config.yaml", "config.nile.yaml"} {
		cfg, err := Load(filepath.Join("..", "..", "configs", name))
		if err != nil {
			t.Fatalf("Load %s: %v", name, err)
		}
		var usdt *TokenConfig
		for i := range cfg.Wallet.Tokens {
			if cfg.Wallet.Tokens[i].Enabled && cfg.Wallet.Tokens[i].Symbol == "USDT" {
				usdt = &cfg.Wallet.Tokens[i]
			}
		}
		if usdt == nil {
			t.Fatalf("%s: no enabled USDT token, the scanner would ignore every transfer", name)
		}
		if usdt.Decimals != 6 {
			t.Fatalf("%s: USDT decimals = %d, want 6", name, usdt.Decimals)
		}
	}
}

func TestLoadExpandsEnvReferences(t *testing.T) {
	t.Setenv("TEST_WALLET_DSN", "secret-dsn")
	cfg, err := Load(writeConfig(t, `
mysql_server:
  dsn: "${TEST_WALLET_DSN}"
redis_server:
  addr: "${TEST_UNSET_REDIS:-127.0.0.1:6379}"
scanner_server:
  chain_nodes:
    - name: fullnode
      endpoint: http://127.0.0.1:8090
      enabled: true
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MySQLCf.DSN != "secret-dsn" {
		t.Fatalf("dsn = %q, want the environment value", cfg.MySQLCf.DSN)
	}
	if cfg.RedisCf.Addr != "127.0.0.1:6379" {
		t.Fatalf("redis addr = %q, want the inline default", cfg.RedisCf.Addr)
	}
}

// An unset reference without a default must expand to empty and then trip
// validation, rather than leaving the literal "${VAR}" in a connection string.
func TestLoadUnsetEnvBecomesEmpty(t *testing.T) {
	_, err := Load(writeConfig(t, `
mysql_server:
  dsn: "${TEST_DEFINITELY_UNSET_DSN}"
scanner_server:
  chain_nodes:
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
mysql_server:
  dsn: "${MYSQL_DSN:-user:pass@tcp(127.0.0.1:3306)/wallet}"
scanner_server:
  chain_nodes:
    - name: fullnode
      enabled: true
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MySQLCf.DSN != "user:pass@tcp(127.0.0.1:3306)/wallet" {
		t.Fatalf("dsn = %q, want the inline default", cfg.MySQLCf.DSN)
	}
}

func TestValidateRequiresAnEnabledNode(t *testing.T) {
	_, err := Load(writeConfig(t, `
mysql_server:
  dsn: "dsn"
scanner_server:
  chain_nodes:
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
mysql_server:
  dsn: "dsn"
scanner_server:
  chain_nodes:
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
	if len(cfg.ScannerServer.ChainNodes) != 2 {
		t.Fatalf("nodes = %d", len(cfg.ScannerServer.ChainNodes))
	}
	// The gateway sorts by priority itself; the loader must keep the values.
	if cfg.ScannerServer.ChainNodes[0].Priority != 2 || cfg.ScannerServer.ChainNodes[1].Priority != 1 {
		t.Fatalf("priorities = %d/%d", cfg.ScannerServer.ChainNodes[0].Priority, cfg.ScannerServer.ChainNodes[1].Priority)
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
mysql_server:
  dsn: "${TEST_WALLET_DSN}"
scanner_server:
  chain_nodes:
    - name: fullnode
      endpoint: "${TEST_NODE_ENDPOINT}"
      enabled: true
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MySQLCf.DSN != "user:p\"a#ss\nword@tcp(127.0.0.1:3306)/wallet" {
		t.Fatalf("dsn = %q, want the raw environment value", cfg.MySQLCf.DSN)
	}
	if cfg.ScannerServer.ChainNodes[0].Endpoint != "http://node:8090 # not a comment" {
		t.Fatalf("endpoint = %q", cfg.ScannerServer.ChainNodes[0].Endpoint)
	}
}

// Plain (unquoted) references must keep their yaml type, so numbers and booleans
// coming from the environment still decode into int / bool fields.
func TestLoadPlainEnvReferenceKeepsType(t *testing.T) {
	t.Setenv("TEST_REDIS_DB", "3")
	t.Setenv("TEST_NODE_ENABLED", "true")
	cfg, err := Load(writeConfig(t, `
mysql_server:
  dsn: "dsn"
redis_server:
  db: ${TEST_REDIS_DB}
scanner_server:
  chain_nodes:
    - name: fullnode
      enabled: ${TEST_NODE_ENABLED}
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RedisCf.DB != 3 {
		t.Fatalf("redis db = %d, want 3", cfg.RedisCf.DB)
	}
	if !cfg.ScannerServer.ChainNodes[0].Enabled {
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
	if cfg.MySQLCf.DSN != os.Getenv("MYSQL_DSN") {
		t.Fatalf("mysql dsn = %q, want the MYSQL_DSN value", cfg.MySQLCf.DSN)
	}

	os.Unsetenv("MYSQL_DSN")
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("Load without MYSQL_DSN: %v", err)
	}
	if !strings.Contains(cfg.MySQLCf.DSN, "127.0.0.1:3306") {
		t.Fatalf("fallback dsn = %q, want the in-file default", cfg.MySQLCf.DSN)
	}
}

func TestRentalDefaultsToOn(t *testing.T) {
	c := &Config{}
	c.applyDefaults()
	if !c.Energy.RentalOn() {
		t.Fatal("RentalOn = false, want true when energy.rental_enabled is unset")
	}
	if c.Energy.Mode != "cheapest" {
		t.Fatalf("mode = %q, want cheapest", c.Energy.Mode)
	}
}

// rental_enabled: false has to leave a config that cannot rent anything: the
// selection is pinned to trx_burn and both rental-only loops are off, whatever
// the rest of the energy section says.
func TestRentalDisabledPinsBurnAndStopsRentalLoops(t *testing.T) {
	off := false
	c := &Config{Energy: EnergyConfig{
		RentalEnabled: &off,
		Mode:          "cheapest",
		Providers: map[string]ProviderConf{
			"gasstation": {Enabled: true},
		},
		Pool:      EnergyPoolConfig{Enabled: true},
		AutoTopup: AutoTopupConfig{Enabled: true, SourceAddress: "TWd4WrZ9wn84f5x1hZhL4DHvk738ns5jwb"},
	}}
	c.applyDefaults()
	if c.Energy.Mode != "fixed" || c.Energy.Fixed != ProviderTRXBurn {
		t.Fatalf("mode/fixed = %q/%q, want fixed/%s", c.Energy.Mode, c.Energy.Fixed, ProviderTRXBurn)
	}
	if !c.Energy.Providers[ProviderTRXBurn].Enabled {
		t.Fatal("trx_burn provider is not enabled")
	}
	if c.Energy.Pool.Enabled || c.Energy.AutoTopup.Enabled {
		t.Fatalf("pool=%v auto_topup=%v, want both disabled",
			c.Energy.Pool.Enabled, c.Energy.AutoTopup.Enabled)
	}
}

// Sweep and withdraw each choose their own energy source, and an unset flow key
// keeps following the global energy.rental_enabled.
func TestPerFlowEnergyRentalFallsBackToGlobal(t *testing.T) {
	off := false
	c := &Config{Energy: EnergyConfig{RentalEnabled: &off}}
	c.applyDefaults()
	if c.SweepRentalOn() || c.WithdrawRentalOn() {
		t.Fatalf("sweep=%v withdraw=%v, want both off", c.SweepRentalOn(), c.WithdrawRentalOn())
	}
	on := true
	c = &Config{Energy: EnergyConfig{RentalEnabled: &on}}
	c.applyDefaults()
	if !c.SweepRentalOn() || !c.WithdrawRentalOn() {
		t.Fatalf("sweep=%v withdraw=%v, want both on", c.SweepRentalOn(), c.WithdrawRentalOn())
	}
}

// Renting for sweeps while withdrawals burn TRX has to keep the rental stack
// built for sweeps, price the burn through trx_burn, and drop the hot wallet
// energy pool that only serves rented withdrawals.
func TestSweepRentsWhileWithdrawBurns(t *testing.T) {
	on, off := true, false
	c := &Config{
		SweepServer:    SweepConfig{EnergyRental: &on},
		WithdrawServer: WithdrawConfig{EnergyRental: &off},
		Energy: EnergyConfig{
			Mode:      "cheapest",
			Providers: map[string]ProviderConf{"gasstation": {Enabled: true}},
			Pool:      EnergyPoolConfig{Enabled: true},
		},
	}
	c.applyDefaults()
	if !c.SweepRentalOn() || c.WithdrawRentalOn() {
		t.Fatalf("sweep=%v withdraw=%v, want sweep renting and withdraw burning",
			c.SweepRentalOn(), c.WithdrawRentalOn())
	}
	if c.Energy.Mode != "cheapest" || !c.Energy.RentalOn() {
		t.Fatalf("mode=%q rental=%v, want the rental stack kept for sweeps",
			c.Energy.Mode, c.Energy.RentalOn())
	}
	if !c.Energy.Providers[ProviderTRXBurn].Enabled {
		t.Fatal("trx_burn provider is not enabled, the withdrawal burn cannot be priced")
	}
	if c.Energy.Pool.Enabled {
		t.Fatal("hot wallet energy pool is still on for withdrawals that burn TRX")
	}
}

// A business request carries the symbol in whatever case the merchant sent it,
// so resolving it must ignore case and must never return a disabled token.
func TestEnabledTokenResolvesSymbolCaseInsensitively(t *testing.T) {
	c := &Config{Wallet: WalletConfig{Tokens: []TokenConfig{
		{Symbol: "USDT", Contract: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", Decimals: 6, Enabled: true},
		{Symbol: "USDC", Contract: "TEkxiTehnzSmSe2XqrBj4w32RUN966rdz8", Decimals: 6, Enabled: true},
		{Symbol: "TUSD", Contract: "TUpMhErZL2fhh4sVNULAbNKLokS4GjC1F4", Decimals: 6},
	}}}
	if len(c.EnabledTokens()) != 2 {
		t.Fatalf("enabled tokens = %d, want 2", len(c.EnabledTokens()))
	}
	token, ok := c.EnabledToken("usdc")
	if !ok || token.Contract != "TEkxiTehnzSmSe2XqrBj4w32RUN966rdz8" {
		t.Fatalf("EnabledToken(usdc) = %+v, %v", token, ok)
	}
	if _, ok := c.EnabledToken("tusd"); ok {
		t.Fatal("a disabled token must not resolve")
	}
}
