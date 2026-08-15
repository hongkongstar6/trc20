// Package bootstrap wires the shared dependencies every entrypoint needs, so
// each cmd/* main stays a thin composition root.
package bootstrap

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/hongkongstar6/trc20/internal/bloom"
	"github.com/hongkongstar6/trc20/internal/chain"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/energy"
	"github.com/hongkongstar6/trc20/internal/logx"
	"github.com/hongkongstar6/trc20/internal/signer"
	"github.com/hongkongstar6/trc20/internal/store"
	"github.com/sirupsen/logrus"

	// Providers self register through their init functions; the registry keeps
	// the rest of the system free of provider specific imports.
	_ "github.com/hongkongstar6/trc20/internal/energy/gasstation"
	_ "github.com/hongkongstar6/trc20/internal/energy/tronenergyrent"
	_ "github.com/hongkongstar6/trc20/internal/energy/trxburn"
)

// defaultConfigName is the config used when neither -config nor CONFIG_PATH
// says otherwise.
const defaultConfigName = "configs/config.nile.yaml"

type App struct {
	//Cfg *config.Config
	//Log     *logrus.Logger //*slog.Logger
	//Store   *store.Store
	Gateway *chain.Gateway //调用https://api.trongrid.io/ 的接口
}

// Init loads the config, opens the datastores and builds the chain gateway.
func Init(service string) (*App, error) {
	path := flag.String("config", envOr("CONFIG_PATH", defaultConfigPath()), "path to the config file")

	//path := flag.String("config", "configs/config.nile.yaml", "path to the config file")

	migrate := flag.Bool("migrate", true, "run schema auto migration and exit")
	flag.Parse()

	_, err := config.Load(*path) //加载配置
	if err != nil {
		logrus.Error("load config failed,", ",err:", err)
		return nil, err
	}
	// Assign the loaded config to the global variable
	logx.InitLogrus(config.Cfg.Log, service)
	st, err := store.Open() //数据库
	if err != nil {
		logrus.Error("DB failed,", ",err:", err)
		return nil, err
	}
	if *migrate && service == "api" {
		if err := st.AutoMigrate(); err != nil {
			logrus.Error("schema migration failed,", ",err:", err)
			return nil, err
		}
		logrus.Info("schema migrated")
		//os.Exit(0)
	}
	// Only the scanner matches on-chain recipients against the filter, so only
	// it loads every deposit address into memory. The other services talk to
	// the scanner over http instead of keeping their own copy.
	if needsAddressFilter(service) {
		if _, err := bloom.Init(context.Background()); err != nil {
			logrus.Error("address bloom filter init failed,", ",err:", err)
			return nil, err
		}
	}
	gw, err := chain.NewGateway(config.Cfg.ScannerServer)
	if err != nil {
		logrus.Error("chain gateway init failed,", ",err:", err)
		return nil, err
	}
	return &App{Gateway: gw}, nil
}

func needsAddressFilter(service string) bool {
	return service == "scanner"
}

// SignerClient builds the sign-service client.
func (a *App) SignerClient() (*signer.Client, error) {
	return signer.NewClient(config.Cfg.SignServer)
}

// EnergyManager builds the provider registry and the manager on top of it.
func (a *App) EnergyManager() (*energy.Manager, error) {
	provs, err := energy.Build(config.Cfg.Energy)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(provs))
	for n := range provs {
		names = append(names, n)
	}
	logrus.Info("energy providers loaded", "providers", names, "mode", config.Cfg.Energy.Mode)
	return energy.NewManager(config.Cfg.Energy, a.Gateway, provs), nil
}

// Context returns a context cancelled on SIGINT/SIGTERM.
func Context() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

// defaultConfigPath finds the nile config by walking up from the working
// directory, so debugging a cmd/* from an IDE works without passing -config.
// The default is deliberately the testnet: mainnet has to be asked for with
// -config configs/config.yaml or CONFIG_PATH, because a process that silently
// picks the mainnet allowlist while talking to nile scans blocks forever
// without ever matching a deposit.
func defaultConfigPath() string {
	if p, ok := config.FindUp("", defaultConfigName); ok {
		return p
	}
	return defaultConfigName
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
