package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hongkongstar6/trc20/internal/tron"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

var Cfg = &Config{}

// Config is the root configuration for every entrypoint of the monorepo.
// Every service reads the same file and only uses the sections it needs.
type Config struct {
	Env      string         `yaml:"env"`
	Network  string         `yaml:"network"` // mainnet | nile
	Log      LogConfig      `yaml:"log"`
	MySQL    MySQLConfig    `yaml:"mysql_server"`
	Redis    RedisConfig    `yaml:"redis_server"`
	API      APIConfig      `yaml:"api_server"`
	Sign     SignConfig     `yaml:"sign_server"`
	Wallet   WalletConfig   `yaml:"wallet"`
	Sweep    SweepConfig    `yaml:"sweep_server"`
	Bloom    BloomConfig    `yaml:"scanner_server"`
	Chain    ChainConfig    `yaml:"chain"`
	Deposit  DepositConfig  `yaml:"deposit"`
	Withdraw WithdrawConfig `yaml:"withdraw_server"`
	Energy   EnergyConfig   `yaml:"energy"`
	Notify   NotifyConfig   `yaml:"notify"`
}

// BloomConfig sizes the in-memory bloom filter holding every deposit address.
// It is only a prefilter: a hit is always resolved against user_wallet, so a
// false positive costs one query and a too small filter only costs throughput.
type BloomConfig struct {
	// ExpectedAddresses is the address count the filter is sized for. It is
	// grown automatically (rebuild at twice the capacity) once exceeded.
	ExpectedAddresses int64   `yaml:"expected_addresses"`
	FalsePositiveRate float64 `yaml:"false_positive_rate"`
	// Listen is the address sync port of the scanner process; NotifyURL is the
	// same endpoint as seen by the api process, which pushes every freshly
	// allocated address to it. Token authenticates that push.
	Listen         string `yaml:"listen"`
	BloomNotifyURL string `yaml:"bloom_notify_url"`
	Token          string `yaml:"token"`
	NotifyTimeout  string `yaml:"notify_timeout"`
	// SyncInterval is the fallback for a push that did not arrive (scanner
	// restart, network blip): the process re-reads user_wallet by id.
	SyncInterval string `yaml:"sync_interval"`
	LoadBatch    int    `yaml:"load_batch"`
}

type LogConfig struct {
	Level string `yaml:"level"`
	// LogDir is the directory log files are written to. When empty logs go to
	// stdout only. Files are rotated daily and named "<service>-YYYY-MM-DD.log".
	LogDir         string `yaml:"log_dir"`
	Output_Console bool   `yaml:"output_console"` //是否输出至控制台
	Output_File    bool   `yaml:"output_file"`    //是否支持输出至文件

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
	Mnemonic   string `yaml:"mnemonic"`   //助记词
	Passphrase string `yaml:"passphrase"` //口令
	TLS        struct {
		Enabled    bool   `yaml:"enabled"`
		CertFile   string `yaml:"cert_file"`
		KeyFile    string `yaml:"key_file"`
		CAFile     string `yaml:"ca_file"`
		ServerName string `yaml:"server_name"`
	} `yaml:"tls"`
}

type ChainConfig struct {
	Nodes []NodeConfig `yaml:"nodes"`
	// SolidityForConfirm reads confirmed data from the solidity node path.
	SolidityForConfirm bool   `yaml:"solidity_for_confirm"`
	RetryPerNode       int    `yaml:"retry_per_node"`
	BroadcastTimeout   string `yaml:"broadcast_timeout"`
	// RateLimitWait caps how long one call waits for a throttled node to
	// reopen before it reports a failure.
	RateLimitWait string `yaml:"rate_limit_wait"`
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
	// QPS throttles outgoing requests to this node. TronGrid counts requests
	// per API key and suspends the key for tens of seconds once the limit is
	// exceeded, so pacing below it is faster than being suspended. 0 means the
	// built in default for trongrid nodes and unlimited for a self hosted
	// FullNode; a negative value disables throttling explicitly.
	QPS float64 `yaml:"qps"`
	// Burst is how many requests may be issued back to back before pacing.
	Burst int `yaml:"burst"`
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
	Tokens      []TokenConfig `yaml:"tokens"`       //
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
	StartBlock  int64 `yaml:"start_block"`
	BatchBlocks int64 `yaml:"batch_blocks"`
	// How many blocks of the batch are downloaded in parallel. One block needs
	// two RPC calls, so a serial scanner cannot outrun the chain itself.
	FetchConcurrency int64  `yaml:"fetch_concurrency"`
	Confirmations    int64  `yaml:"confirmations"`
	PollInterval     string `yaml:"poll_interval"`
	ReorgDepth       int64  `yaml:"reorg_depth"`
	MinDepositUnits  string `yaml:"min_deposit_units"` // dust filter, min units
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
	Enabled bool `yaml:"enabled"`
	// Interval is the delay between two sweep rounds, e.g. "3600s" or "1h".
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
	// FixedUSDT pins the threshold to a constant amount of USDT, e.g. 100 to
	// leave every wallet holding less than 100 USDT alone. It disables the
	// runtime break-even computation entirely; 0 keeps that computation.
	FixedUSDT float64 `yaml:"fixed_usdt"`
	// MaxSkipRounds force-sweeps an address that was skipped for being below
	// the threshold that many rounds in a row, so a wallet that never reaches
	// the threshold still reaches the finance wallet. 0 disables it.
	MaxSkipRounds int `yaml:"max_skip_rounds"`
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
	// HTTP struct {
	// 	Enabled bool   `yaml:"enabled"`
	// 	URL     string `yaml:"url"`
	// 	Secret  string `yaml:"secret"`
	// 	Timeout string `yaml:"timeout"`
	// } `yaml:"http"`
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
//
// The yaml is parsed first and the expansion happens on the parsed scalars, so
// an env value carrying quotes, colons, '#' or newlines can never change the
// document structure.
func Load1(path string) (*Config, error) {
	_ = godotenv.Load(".env")

	// 2. 配置 Viper 读取 YAML
	viper.SetConfigFile("configs/config.yaml")

	// viper.SetConfigName("config")
	// viper.SetConfigType("yaml")
	// viper.AddConfigPath("./config")
	// 开启环境变量替换功能
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("读取配置文件失败: %v", err)
	}

	// 核心步骤：告诉 Viper 替换配置文件中的 ${VAR} 占位符
	// 注意：需要在反序列化或获取值时确保变量已被替换
	//var config Config
	if err := viper.Unmarshal(&Cfg); err != nil {
		log.Fatalf("解析配置失败: %v", err)
	}
	return Cfg, nil
}

func Load(path string) (*Config, error) {
	if err := loadEnvFileFor(path); err != nil {
		return nil, fmt.Errorf("load env file: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse config %s: %w%s", path, err, sourceHint(raw, err))
	}
	if doc.Kind == 0 {
		return nil, fmt.Errorf("parse config %s: file is empty", path)
	}
	if err := checkSections(&doc); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	expandEnvNode(&doc)
	//var c Config
	if err := doc.Decode(Cfg); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	//Cfg = &c
	Cfg.applyDefaults()
	if err := Cfg.validate(); err != nil {
		return nil, err
	}
	return Cfg, nil
}

// checkSections rejects a top level key that no field of Config maps to. yaml
// silently ignores an unmapped key, so a section renamed in the file but not in
// the struct (or the other way round) would leave that whole part of the config
// at its zero value: an empty wallet.tokens allowlist makes the scanner drop
// every Transfer log it sees, which looks like deposits that never arrive.
func checkSections(doc *yaml.Node) error {
	root := doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil
		}
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil
	}
	known := configSections()
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if _, ok := known[key]; !ok {
			return fmt.Errorf("unknown section %q (known sections: %s)", key, strings.Join(sortedKeys(known), ", "))
		}
	}
	return nil
}

func configSections() map[string]struct{} {
	t := reflect.TypeOf(Config{})
	out := make(map[string]struct{}, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0]
		if name == "" {
			name = strings.ToLower(t.Field(i).Name)
		}
		out[name] = struct{}{}
	}
	return out
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// expandEnvNode substitutes ${ENV} / ${ENV:-default} inside every scalar of the
// document. Plain scalars drop their resolved tag so "${PORT}" -> 8080 is still
// decoded as a number, quoted scalars stay strings.
func expandEnvNode(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode {
		v := expandEnvString(n.Value)
		if v != n.Value {
			n.Value = v
			if n.Style == yaml.DoubleQuotedStyle || n.Style == yaml.SingleQuotedStyle {
				n.Tag = "!!str"
			} else {
				n.Tag = ""
				n.Style = 0
			}
		}
	}
	for _, child := range n.Content {
		expandEnvNode(child)
	}
}

func expandEnvString(s string) string {
	return envPattern.ReplaceAllStringFunc(s, func(m string) string {
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
}

var yamlErrLine = regexp.MustCompile(`line (\d+):`)

// sourceHint appends the offending source line to a yaml syntax error, so a
// "did not find expected key" points at something actionable.
func sourceHint(raw []byte, err error) string {
	m := yamlErrLine.FindStringSubmatch(err.Error())
	if m == nil {
		return ""
	}
	n, convErr := strconv.Atoi(m[1])
	if convErr != nil {
		return ""
	}
	lines := strings.Split(string(raw), "\n")
	if n < 1 || n > len(lines) {
		return ""
	}
	return fmt.Sprintf(" (line %d: %q)", n, lines[n-1])
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
	if c.Deposit.FetchConcurrency <= 0 {
		c.Deposit.FetchConcurrency = 8
	}
	if c.Deposit.FetchConcurrency > c.Deposit.BatchBlocks {
		c.Deposit.FetchConcurrency = c.Deposit.BatchBlocks
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
	if c.Bloom.ExpectedAddresses <= 0 {
		c.Bloom.ExpectedAddresses = 200000
	}
	if c.Bloom.FalsePositiveRate <= 0 || c.Bloom.FalsePositiveRate >= 1 {
		c.Bloom.FalsePositiveRate = 0.0001
	}
	if c.Bloom.LoadBatch <= 0 {
		c.Bloom.LoadBatch = 5000
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
	// Without an enabled token the deposit scanner has an empty contract
	// allowlist and silently ignores every transfer on chain.
	tokens := 0
	for _, t := range c.Wallet.Tokens {
		if !t.Enabled {
			continue
		}
		if !tron.IsValidAddress(t.Contract) {
			return fmt.Errorf("wallet.tokens[%s].contract %q is not a base58 TRON address", t.Symbol, t.Contract)
		}
		if t.Decimals <= 0 {
			return fmt.Errorf("wallet.tokens[%s].decimals is required", t.Symbol)
		}
		tokens++
	}
	if tokens == 0 {
		return fmt.Errorf("wallet.tokens: at least one enabled token is required")
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
