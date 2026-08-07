package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration for every entrypoint of the monorepo.
// Every service reads the same file and only uses the sections it needs.
type Config struct {
	Env      string         `yaml:"env"`
	Network  string         `yaml:"network"` // mainnet | nile
	Log      LogConfig      `yaml:"log"`
	MySQL    MySQLConfig    `yaml:"mysql"`
	Redis    RedisConfig    `yaml:"redis"`
	API      APIConfig      `yaml:"api"`
	Sign     SignConfig     `yaml:"sign"`
	Chain    ChainConfig    `yaml:"chain"`
	Wallet   WalletConfig   `yaml:"wallet"`
	Deposit  DepositConfig  `yaml:"deposit"`
	Withdraw WithdrawConfig `yaml:"withdraw"`
	Sweep    SweepConfig    `yaml:"sweep"`
	Energy   EnergyConfig   `yaml:"energy"`
	Notify   NotifyConfig   `yaml:"notify"`
}

type LogConfig struct {
	Level string `yaml:"level"`
	// LogDir is the directory log files are written to. When empty logs go to
	// stdout only. Files are rotated daily and named "<service>-YYYY-MM-DD.log".
	LogDir string `yaml:"log_dir"`
}

type MySQLConfig struct {
	DSN             string `yaml:"dsn"`
	MaxOpenConns    int    `yaml:"max_open_conns"`
	MaxIdleConns    int    `yaml:"max_idle_conns"`
	ConnMaxLifetime string `yaml:"conn_max_lifetime"`
}

type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	Prefix   string `yaml:"prefix"`
}

type APIConfig struct {
	Listen string `yaml:"listen"`
	// HMACSecret authenticates the business system. Requests carry
	// X-Timestamp / X-Nonce / X-Signature.
	HMACSecret     string   `yaml:"hmac_secret"`
	AllowedIPs     []string `yaml:"allowed_ips"`
	SignatureSkew  string   `yaml:"signature_skew"`
	NonceTTL       string   `yaml:"nonce_ttl"`
	MaxBodyBytes   int64    `yaml:"max_body_bytes"`
	RequestTimeout string   `yaml:"request_timeout"`
}

type SignConfig struct {
	// Listen is used by cmd/sign; Endpoint is used by the callers.
	Listen   string `yaml:"listen"`
	Endpoint string `yaml:"endpoint"`
	Token    string `yaml:"token"`
	// Mnemonic must come from a secret manager / env var, never from git.
	Mnemonic   string `yaml:"mnemonic"`
	Passphrase string `yaml:"passphrase"`
	TLS        struct {
		Enabled    bool   `yaml:"enabled"`
		CertFile   string `yaml:"cert_file"`
		KeyFile    string `yaml:"key_file"`
		CAFile     string `yaml:"ca_file"`
		ServerName string `yaml:"server_name"`
	} `yaml:"tls"`
}

type NodeConfig struct {
	Name     string            `yaml:"name"`
	Type     string            `yaml:"type"` // trongrid | fullnode
	Endpoint string            `yaml:"endpoint"`
	APIKey   string            `yaml:"api_key"`
	Priority int               `yaml:"priority"` // smaller wins
	Enabled  bool              `yaml:"enabled"`
	Headers  map[string]string `yaml:"headers"`
	Timeout  string            `yaml:"timeout"`
}

type ChainConfig struct {
	Nodes []NodeConfig `yaml:"nodes"`
	// SolidityForConfirm reads confirmed data from the solidity node path.
	SolidityForConfirm bool   `yaml:"solidity_for_confirm"`
	RetryPerNode       int    `yaml:"retry_per_node"`
	BroadcastTimeout   string `yaml:"broadcast_timeout"`
}

type TokenConfig struct {
	Symbol   string `yaml:"symbol"`
	Contract string `yaml:"contract"`
	Decimals int    `yaml:"decimals"`
	Enabled  bool   `yaml:"enabled"`
}

type WalletConfig struct {
	// TRON BIP44 coin type is 195.
	AccountPath string        `yaml:"account_path"` // e.g. m/44'/195'/0'/0
	Tokens      []TokenConfig `yaml:"tokens"`
	// Named system addresses. Their derivation paths are fixed and never
	// reused by user deposit addresses.
	HotWallet     NamedAddress `yaml:"hot_wallet"`
	FinanceWallet NamedAddress `yaml:"finance_wallet"`
	GasAccount    NamedAddress `yaml:"gas_account"`
	IndexBatch    int          `yaml:"index_batch"`
}

type NamedAddress struct {
	Address string `yaml:"address"`
	Path    string `yaml:"path"`
}

type DepositConfig struct {
	StartBlock      int64  `yaml:"start_block"`
	BatchBlocks     int64  `yaml:"batch_blocks"`
	Confirmations   int64  `yaml:"confirmations"`
	PollInterval    string `yaml:"poll_interval"`
	ReorgDepth      int64  `yaml:"reorg_depth"`
	MinDepositUnits string `yaml:"min_deposit_units"` // dust filter, min units
}

type WithdrawConfig struct {
	Enabled          bool     `yaml:"enabled"`
	PollInterval     string   `yaml:"poll_interval"`
	FeeLimitSun      int64    `yaml:"fee_limit_sun"`
	TxExpirationSec  int64    `yaml:"tx_expiration_sec"`
	MaxAmountUnits   string   `yaml:"max_amount_units"`
	DailyMaxUnits    string   `yaml:"daily_max_units"`
	ConfirmBlocks    int64    `yaml:"confirm_blocks"`
	BroadcastRetries int      `yaml:"broadcast_retries"`
	AddressBlacklist []string `yaml:"address_blacklist"`
}

type SweepConfig struct {
	Enabled     bool                 `yaml:"enabled"`
	Interval    string               `yaml:"interval"`
	FeeLimitSun int64                `yaml:"fee_limit_sun"`
	Threshold   SweepThresholdConfig `yaml:"threshold"`
	MaxPerRound int                  `yaml:"max_per_round"`
	LockTTL     string               `yaml:"lock_ttl"`
}

// SweepThresholdConfig drives the runtime break-even computation of min_sweep.
// min_sweep_usd = cost_usd / target_cost_ratio, clamped into [min,max].
type SweepThresholdConfig struct {
	TargetCostRatio float64 `yaml:"target_cost_ratio"`
	SafetyMultiple  float64 `yaml:"safety_multiple"`
	MinUSDT         float64 `yaml:"min_usdt"`
	MaxUSDT         float64 `yaml:"max_usdt"`
	RefreshInterval string  `yaml:"refresh_interval"`
	TRXPriceUSD     float64 `yaml:"trx_price_usd"`
	TRXPriceURL     string  `yaml:"trx_price_url"`
	// StaleDays force-sweeps addresses that stayed below the threshold for
	// too long so dust cannot accumulate forever.
	StaleDays int `yaml:"stale_days"`
}

type EnergyConfig struct {
	// Mode: cheapest | priority | fixed
	Mode           string                  `yaml:"mode"`
	Fixed          string                  `yaml:"fixed"`
	QuoteCacheTTL  string                  `yaml:"quote_cache_ttl"`
	DefaultPeriod  string                  `yaml:"default_period"`
	EnergyPerTx    int64                   `yaml:"energy_per_tx"`     // recipient already holds the token
	EnergyPerTxNew int64                   `yaml:"energy_per_tx_new"` // recipient never held the token
	WaitTimeout    string                  `yaml:"wait_timeout"`
	Providers      map[string]ProviderConf `yaml:"providers"`
	Pool           EnergyPoolConfig        `yaml:"pool"`
	AutoTopup      AutoTopupConfig         `yaml:"auto_topup"`
}

type ProviderConf struct {
	Enabled bool `yaml:"enabled"`
	// Options carries provider specific settings so a new provider can be
	// added without touching this struct.
	Options map[string]string `yaml:"options"`
}

// EnergyPoolConfig keeps the withdrawal hot wallet topped up with delegated
// energy in batches instead of renting once per withdrawal.
type EnergyPoolConfig struct {
	Enabled       bool   `yaml:"enabled"`
	CheckInterval string `yaml:"check_interval"`
	LowWaterTxs   int64  `yaml:"low_water_txs"`
	BatchTxs      int64  `yaml:"batch_txs"`
	Period        string `yaml:"period"`
	MaxBatchTxs   int64  `yaml:"max_batch_txs"`
}

// AutoTopupConfig refills the prepaid balance of each rental provider from a
// dedicated small gas account (never from the finance cold wallet).
type AutoTopupConfig struct {
	Enabled       bool                         `yaml:"enabled"`
	SourceAddress string                       `yaml:"source_address"`
	CheckInterval string                       `yaml:"check_interval"`
	GasAccount    GasAccountConfig             `yaml:"gas_account"`
	Providers     map[string]ProviderTopupConf `yaml:"providers"`
}

type GasAccountConfig struct {
	TargetTRX       float64 `yaml:"target_trx"`
	LowWatermarkTRX float64 `yaml:"low_watermark_trx"`
}

type ProviderTopupConf struct {
	Enabled            bool    `yaml:"enabled"`
	LowWatermarkTRX    float64 `yaml:"low_watermark_trx"`
	TargetTRX          float64 `yaml:"target_trx"`
	MaxSingleTopupTRX  float64 `yaml:"max_single_topup_trx"`
	MaxDailyTopupTRX   float64 `yaml:"max_daily_topup_trx"`
	MaxDailyTopupCount int     `yaml:"max_daily_topup_count"`
	// DepositAddress is the hard whitelist. The provider reported address is
	// re-fetched before every transfer and must match this value.
	DepositAddress string `yaml:"deposit_address"`
}

type NotifyConfig struct {
	// Outbox delivery: http callback and/or rocketmq.
	HTTP struct {
		Enabled bool   `yaml:"enabled"`
		URL     string `yaml:"url"`
		Secret  string `yaml:"secret"`
		Timeout string `yaml:"timeout"`
	} `yaml:"http"`
	RocketMQ struct {
		Enabled    bool     `yaml:"enabled"`
		NameServer []string `yaml:"name_server"`
		Topic      string   `yaml:"topic"`
		Group      string   `yaml:"group"`
	} `yaml:"rocketmq"`
	BatchSize    int    `yaml:"batch_size"`
	PollInterval string `yaml:"poll_interval"`
	MaxRetry     int    `yaml:"max_retry"`
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// Load reads a yaml config and expands ${ENV} / ${ENV:-default} references so
// secrets stay outside of the repository. Before expanding it sources a .env
// file (ENV_FILE, else the nearest .env next to the config or above the working
// directory) so running from an IDE behaves like docker compose.
func Load(path string) (*Config, error) {
	if err := loadEnvFileFor(path); err != nil {
		return nil, fmt.Errorf("load env file: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	expanded := envPattern.ReplaceAllStringFunc(string(raw), func(m string) string {
		g := envPattern.FindStringSubmatch(m)
		v, ok := os.LookupEnv(g[1])
		// ${VAR:-default} follows POSIX semantics: the default applies when the
		// variable is unset OR set to the empty string, so an exported but empty
		// MYSQL_DSN=... still falls back instead of tripping validation.
		if g[2] != "" {
			if !ok || v == "" {
				return g[3]
			}
			return v
		}
		// ${VAR} has no default: expand to the value or empty.
		if ok {
			return v
		}
		return ""
	})
	var c Config
	if err := yaml.Unmarshal([]byte(expanded), &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// loadEnvFileFor resolves which .env to source for the given config path.
func loadEnvFileFor(configPath string) error {
	if explicit := os.Getenv("ENV_FILE"); explicit != "" {
		return LoadDotEnv(explicit)
	}
	var start string
	if abs, err := filepath.Abs(configPath); err == nil {
		start = filepath.Dir(abs)
	}
	if p, ok := FindUp(start, ".env"); ok {
		return LoadDotEnv(p)
	}
	if p, ok := FindUp("", ".env"); ok {
		return LoadDotEnv(p)
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.Network == "" {
		c.Network = "nile"
	}
	if c.Wallet.AccountPath == "" {
		c.Wallet.AccountPath = "m/44'/195'/0'/0"
	}
	if c.Wallet.IndexBatch <= 0 {
		c.Wallet.IndexBatch = 1
	}
	if c.Deposit.BatchBlocks <= 0 {
		c.Deposit.BatchBlocks = 20
	}
	if c.Deposit.Confirmations <= 0 {
		c.Deposit.Confirmations = 19
	}
	if c.Deposit.ReorgDepth <= 0 {
		c.Deposit.ReorgDepth = 60
	}
	if c.Energy.Mode == "" {
		c.Energy.Mode = "cheapest"
	}
	if c.Energy.EnergyPerTx <= 0 {
		c.Energy.EnergyPerTx = 32000
	}
	if c.Energy.EnergyPerTxNew <= 0 {
		c.Energy.EnergyPerTxNew = 65000
	}
	if c.Notify.BatchSize <= 0 {
		c.Notify.BatchSize = 100
	}
	if c.Notify.MaxRetry <= 0 {
		c.Notify.MaxRetry = 10
	}
	if c.Sweep.Threshold.TargetCostRatio <= 0 {
		c.Sweep.Threshold.TargetCostRatio = 0.005
	}
	if c.Sweep.Threshold.SafetyMultiple <= 0 {
		c.Sweep.Threshold.SafetyMultiple = 20
	}
	if c.Sweep.MaxPerRound <= 0 {
		c.Sweep.MaxPerRound = 50
	}
}

func (c *Config) validate() error {
	if c.MySQL.DSN == "" {
		return fmt.Errorf("mysql.dsn is required")
	}
	enabled := 0
	for _, n := range c.Chain.Nodes {
		if n.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return fmt.Errorf("chain.nodes: at least one enabled node is required")
	}
	if c.Energy.Mode == "fixed" && c.Energy.Fixed == "" {
		return fmt.Errorf("energy.fixed is required when energy.mode=fixed")
	}
	if c.Energy.AutoTopup.Enabled {
		if c.Energy.AutoTopup.SourceAddress == "" {
			return fmt.Errorf("energy.auto_topup.source_address is required when auto topup is enabled")
		}
		for name, p := range c.Energy.AutoTopup.Providers {
			if p.Enabled && p.DepositAddress == "" {
				return fmt.Errorf("energy.auto_topup.providers.%s.deposit_address is required", name)
			}
		}
	}
	return nil
}

// Duration parses a duration string with a fallback, so config typos degrade
// to a safe value instead of a zero ticker.
func Duration(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return def
	}
	return d
}
